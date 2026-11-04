package ytr

import (
	"context"
	"errors"
	"fmt"
	"mime"
	"net/url"
	"strconv"
	"strings"
)

type linkState struct{ angle, quoted, escaped bool }

// NextLink reads Link rel=next, including multiple Link headers and relation
// lists. It returns an untrusted URL; Paginate validates it before sending auth.
// Links with an anchor parameter are ignored; anchor contexts are unsupported.
func (r *Response) NextLink() (string, error) {
	var next string

	for _, header := range r.Header.Values("Link") {
		links, err := splitLinks(header)
		if err != nil {
			return "", err
		}

		for _, link := range links {
			target, err := parseNextLink(link)
			if err != nil {
				return "", err
			}

			if target == "" {
				continue
			}

			if next != "" {
				return "", errors.New("ytr: multiple next links")
			}

			next = target
		}
	}

	return next, nil
}

// Paginate visits JSON responses of GET operations and POST search/list
// operations. It follows same-resource Link headers or X-Total-Pages. The HTTP
// method and JSON body are preserved on every page. Cursor-in-body APIs remain
// explicit calls: their pagination conventions differ. Return ErrStopPagination
// from visit to finish successfully. Path parameters follow catalog order.
func (c *Client) Paginate(ctx context.Context, name OperationName, args []string, body any, visit func(*Response) error, options ...CallOption) error {
	operation, err := LookupOperation(name)
	if err != nil {
		return err
	}

	if err = validatePagination(operation, visit); err != nil {
		return err
	}

	if operation.Request == "none" && body != nil {
		return fmt.Errorf("%w: %s does not accept a request body", ErrOperationKind, operation.Name)
	}

	path, err := renderPath(operation.Path, args)
	if err != nil {
		return err
	}

	data, err := encodeJSON(body)
	if err != nil {
		return err
	}

	opts, err := optionsFor(options)
	if err != nil {
		return err
	}

	return c.visitPages(ctx, operation.Method, path, data, opts, visit)
}

func (c *Client) visitPages(ctx context.Context, method, path string, body []byte, options callOptions, visit func(*Response) error) error {
	seen := map[string]bool{}

	for {
		key := options.query.Encode()
		if seen[key] {
			return ErrPaginationCycle
		}

		seen[key] = true

		response, err := c.jsonRequest(ctx, method, path, body, options)
		if err != nil {
			return err
		}

		if err = visit(response); err != nil {
			return paginationVisitorError(err)
		}

		next, err := c.nextQuery(response, path, options.query)
		if err != nil {
			return err
		}

		if next == nil {
			return nil
		}

		options.query = next
	}
}

func (c *Client) nextQuery(response *Response, path string, current url.Values) (url.Values, error) {
	link, err := response.NextLink()
	if err != nil {
		return nil, err
	}

	if link != "" {
		return c.safeNextQuery(path, link)
	}

	return numberedNext(response.Header.Get("X-Total-Pages"), current)
}

func (c *Client) safeNextQuery(path, link string) (url.Values, error) {
	if strings.HasPrefix(link, c.base.Host+"/") {
		link = c.base.Scheme + "://" + link
	}

	relative, err := url.Parse(link)
	if err != nil {
		return nil, ErrUnsafeLink
	}

	base, err := url.Parse(c.base.String() + path)
	if err != nil {
		return nil, err
	}

	next := base.ResolveReference(relative)
	if next.Scheme != base.Scheme || !strings.EqualFold(next.Host, base.Host) {
		return nil, ErrUnsafeLink
	}

	if !sameResource(next, base) {
		return nil, ErrUnsafeLink
	}

	query, err := url.ParseQuery(next.RawQuery)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid pagination query: %w", ErrUnsafeLink, err)
	}

	return query, nil
}

func (s *linkState) separator(ch byte) bool {
	if s.escaped {
		s.escaped = false
		return false
	}

	if s.quoted {
		return s.inQuote(ch)
	}

	switch ch {
	case '<':
		s.angle = true
	case '>':
		s.angle = false
	case '"':
		s.quoted = true
	case ',':
		return !s.angle
	}

	return false
}

func (s *linkState) inQuote(ch byte) bool {
	if ch == '\\' {
		s.escaped = true
	}

	if ch == '"' {
		s.quoted = false
	}

	return false
}

func parseNextLink(link string) (string, error) {
	link = strings.TrimSpace(link)

	end := strings.IndexByte(link, '>')
	if !strings.HasPrefix(link, "<") || end < 2 {
		return "", errors.New("ytr: malformed Link header")
	}

	_, params, err := mime.ParseMediaType("link" + link[end+1:])
	if err != nil {
		return "", fmt.Errorf("ytr: malformed Link parameters: %w", err)
	}

	if _, anchored := params["anchor"]; anchored {
		return "", nil
	}

	for relation := range strings.FieldsSeq(params["rel"]) {
		if strings.EqualFold(relation, "next") {
			return link[1:end], nil
		}
	}

	return "", nil
}

func splitLinks(header string) ([]string, error) {
	var result []string

	start := 0

	state := linkState{}
	for i := range len(header) {
		if state.separator(header[i]) {
			result = append(result, header[start:i])
			start = i + 1
		}
	}

	if state.angle || state.quoted {
		return nil, errors.New("ytr: unterminated Link header")
	}

	return append(result, header[start:]), nil
}

func validatePagination(op Operation, visit func(*Response) error) error {
	if visit == nil || op.Response != "json" {
		return fmt.Errorf("%w: pagination needs a JSON operation and visitor", ErrOperationKind)
	}

	if op.Method == "GET" {
		return nil
	}

	if op.Method == "POST" && (strings.HasSuffix(op.Path, "/_search") || strings.HasSuffix(op.Path, "/_list")) {
		return nil
	}

	return fmt.Errorf("%w: pagination is limited to read operations", ErrOperationKind)
}

func paginationVisitorError(err error) error {
	if errors.Is(err, ErrStopPagination) {
		return nil
	}

	return err
}

func sameResource(next, base *url.URL) bool {
	return next.User == nil && next.Fragment == "" && next.Opaque == "" && next.EscapedPath() == base.EscapedPath()
}

func numberedNext(raw string, current url.Values) (url.Values, error) {
	if raw == "" {
		return nil, nil
	}

	total, err := strconv.Atoi(raw)
	if err != nil || total < 0 {
		return nil, errors.New("ytr: invalid X-Total-Pages header")
	}

	page, err := currentPage(current.Get("page"))
	if err != nil {
		return nil, err
	}

	if page >= total {
		return nil, nil
	}

	next := cloneValues(current)
	next.Set("page", strconv.Itoa(page+1))

	return next, nil
}

func currentPage(raw string) (int, error) {
	if raw == "" {
		return 1, nil
	}

	page, err := strconv.Atoi(raw)
	if err != nil || page < 1 {
		return 0, errors.New("ytr: invalid page number")
	}

	return page, nil
}
