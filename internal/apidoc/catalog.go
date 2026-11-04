// Package apidoc owns the offline, reviewable documentation inventory.
package apidoc

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"go/token"
	"io"
	"os"
	"regexp"
	"slices"
	"strings"
)

var (
	operationNamePattern = regexp.MustCompile(`^[A-Z][A-Za-z0-9]+$`)
	pathParameterPattern = regexp.MustCompile(`\{([^}]+)\}`)
	apiPageLinkPattern   = regexp.MustCompile(`\]\((https://yandex\.ru/support/tracker/ru/api/[^)]+)\)`)
)

// Catalog records the reviewed operations and documentation revision.
type Catalog struct {
	Revision   string      `json:"revision"`
	VerifiedOn string      `json:"verified_on"`
	Operations []Operation `json:"operations"`
}

// Operation maps a public SDK method to its documented HTTP contract.
type Operation struct {
	Name     string `json:"name"`
	Method   string `json:"method"`
	Path     string `json:"path"`
	Request  string `json:"request"`
	Response string `json:"response"`
	Source   string `json:"source"`
	Summary  string `json:"summary"`
}

// Sources records the documentation index and the evidence for each API page.
type Sources struct {
	IndexURL string `json:"index"`
	Pages    []Page `json:"pages"`
}

// Page records source hashes, signatures, and associated SDK operations.
type Page struct {
	Path               string          `json:"path"`
	Title              string          `json:"title"`
	URL                string          `json:"url"`
	SHA256             string          `json:"sha256"`
	ContentSHA256      string          `json:"content_sha256"`
	Signatures         []string        `json:"signatures"`
	Operations         []string        `json:"operations"`
	Note               string          `json:"note,omitempty"`
	ExcludedSignatures []RouteDecision `json:"excluded_signatures,omitempty"`
	FieldDecisions     []FieldDecision `json:"field_decisions,omitempty"`
}

// RouteDecision classifies a published example, reference, or source inconsistency.
// Its justification is bound to the exact reviewed page content.
type RouteDecision struct {
	Signature string `json:"signature"`
	Digest    string `json:"digest"`
	Reason    string `json:"reason"`
}

// FieldDecision records a reviewed source/model disagreement or unmapped row.
// Changing the source row or model type invalidates the decision.
type FieldDecision struct {
	Section string `json:"section"`
	Digest  string `json:"digest"`
	Model   string `json:"model,omitempty"`
	Field   string `json:"field,omitempty"`
	Type    string `json:"type,omitempty"`
	Reason  string `json:"reason"`
}

// ReadJSON reads one JSON value from a file, rejecting unknown fields and trailing data.
func ReadJSON(path string, out any) error {
	data, err := os.ReadFile(path) // #nosec G304 -- The caller selects a local inventory file; this path is not taken from HTTP input.
	if err != nil {
		return err
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(out); err != nil {
		return err
	}

	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return errors.New("trailing content in JSON inventory")
	}

	return nil
}

// Load reads and validates a catalog from a JSON file.
func Load(path string) (Catalog, error) {
	var c Catalog
	if err := ReadJSON(path, &c); err != nil {
		return c, err
	}

	return c, Validate(c)
}

// Validate checks catalog metadata, operation names, routes, and encodings.
func Validate(c Catalog) error {
	if c.Revision == "" || c.VerifiedOn == "" || len(c.Operations) == 0 {
		return errors.New("catalog metadata or operations missing")
	}

	names := map[string]bool{}
	routes := map[string]bool{}

	for _, op := range c.Operations {
		if err := validateOperation(op); err != nil {
			return err
		}

		route := op.Method + " " + RouteShape(op.Path)
		if names[op.Name] || routes[route] {
			return fmt.Errorf("duplicate operation: %s", op.Name)
		}

		names[op.Name] = true
		routes[route] = true
	}

	return nil
}

// Check proves that all indexed pages are classified and every independently
// captured HTTP signature has a matching public operation. Supplementary pages
// explicitly refer to the operations they describe.
func Check(c Catalog, s Sources, index string) error {
	if err := Validate(c); err != nil {
		return err
	}

	if err := checkPages(s, index); err != nil {
		return err
	}

	operations := map[string]Operation{}
	sourcePaths := make(map[string]bool, len(s.Pages))

	for _, page := range s.Pages {
		sourcePaths[page.Path] = true
	}

	for _, op := range c.Operations {
		if !sourcePaths[op.Source] {
			return fmt.Errorf("operation %s references missing source page %s", op.Name, op.Source)
		}

		operations[op.Name] = op
	}

	covered := map[string]bool{}
	for _, p := range s.Pages {
		if err := checkPageOperations(p, operations, covered); err != nil {
			return err
		}
	}

	for _, op := range c.Operations {
		if !covered[op.Name] {
			return fmt.Errorf("operation missing documentation signature: %s", op.Name)
		}
	}

	return nil
}

// PathParameters extracts named placeholders from a path template in order.
func PathParameters(path string) []string {
	matches := pathParameterPattern.FindAllStringSubmatch(path, -1)
	if len(matches) == 0 {
		return nil
	}

	result := make([]string, len(matches))
	for i, match := range matches {
		result[i] = match[1]
	}

	return result
}

// RouteShape normalizes placeholder names for HTTP signature comparisons.
func RouteShape(path string) string {
	return pathParameterPattern.ReplaceAllString(strings.TrimRight(path, "/"), "{}")
}

// IndexURLs extracts API-page links from the Tracker Markdown documentation index.
func IndexURLs(index string) map[string]bool {
	urls := map[string]bool{}
	for _, match := range apiPageLinkPattern.FindAllStringSubmatch(index, -1) {
		urls[stripURLQuery(match[1])] = true
	}

	return urls
}

func validateOperation(op Operation) error {
	if !operationNamePattern.MatchString(op.Name) || !strings.HasPrefix(op.Source, "api/") {
		return fmt.Errorf("invalid operation identity: %s", op.Name)
	}

	if !strings.HasPrefix(op.Path, "/v3/") && !strings.HasPrefix(op.Path, "/v2/") {
		return fmt.Errorf("invalid API path: %s", op.Path)
	}

	if err := validateOperationSymbols(op); err != nil {
		return err
	}

	switch op.Method {
	case "GET", "POST", "PUT", "PATCH", "DELETE":
		return validateEncoding(op)
	default:
		return fmt.Errorf("invalid method: %s", op.Method)
	}
}

func validateOperationSymbols(op Operation) error {
	if slices.Contains([]string{"Call", "CallInto", "Upload", "UploadInto", "Download", "Paginate", "Raw"}, op.Name) {
		return fmt.Errorf("operation name %s conflicts with an SDK client method", op.Name)
	}

	if strings.ContainsAny(op.Path, "?#") {
		return fmt.Errorf("operation %s: path must not contain query or fragment", op.Name)
	}

	used := map[string]bool{"c": true, "ctx": true, "options": true, "operations": true, "string": true}
	if op.Request == "json" {
		used["body"] = true
	}

	if op.Request == "multipart" {
		used["file"] = true
	}

	if op.Request == "none" && op.Response == "json" {
		used["nil"] = true
	}

	for _, part := range strings.Split(op.Path, "/") {
		if !strings.ContainsAny(part, "{}") {
			continue
		}

		if len(part) < 3 || part[0] != '{' || part[len(part)-1] != '}' {
			return fmt.Errorf("operation %s: invalid path placeholder %q", op.Name, part)
		}

		name := part[1 : len(part)-1]
		if !token.IsIdentifier(name) || name == "_" || used[name] {
			return fmt.Errorf("operation %s: invalid, duplicate or reserved path parameter %q", op.Name, name)
		}

		used[name] = true
	}

	return nil
}

func validateEncoding(op Operation) error {
	if op.Request != "none" && op.Request != "json" && op.Request != "multipart" {
		return fmt.Errorf("invalid request encoding: %s", op.Name)
	}

	if op.Response != "json" && op.Response != "stream" {
		return fmt.Errorf("invalid response encoding: %s", op.Name)
	}

	if op.Response == "stream" && op.Request != "none" {
		return fmt.Errorf("stream operation %s cannot accept a JSON or multipart body", op.Name)
	}

	return nil
}

func checkPages(s Sources, index string) error {
	expected := IndexURLs(index)
	seen := map[string]bool{}

	for _, p := range s.Pages {
		identity := stripURLQuery(p.URL)
		if identity != "https://yandex.ru/support/tracker/ru/"+p.Path {
			return fmt.Errorf("source path %s does not match URL %s", p.Path, p.URL)
		}

		if seen[identity] || !expected[identity] {
			return fmt.Errorf("unexpected or duplicate source: %s", p.URL)
		}

		if !hasEvidence(p) {
			return fmt.Errorf("invalid page evidence: %s", p.Path)
		}

		if len(p.Operations) == 0 && p.Note == "" {
			return fmt.Errorf("unclassified page: %s", p.Path)
		}

		seen[identity] = true
	}

	if len(seen) != len(expected) {
		return errors.New("indexed pages missing from source inventory")
	}

	return nil
}

func hasEvidence(p Page) bool {
	return p.Path != "" && len(p.SHA256) == 64 && len(p.ContentSHA256) == 64
}

func checkPageOperations(p Page, operations map[string]Operation, covered map[string]bool) error {
	allowed := map[string]Operation{}

	for _, name := range p.Operations {
		op, ok := operations[name]
		if !ok {
			return fmt.Errorf("unknown source operation: %s", name)
		}

		allowed[op.Method+" "+RouteShape(op.Path)] = op
	}

	for _, sig := range p.Signatures {
		op, ok := allowed[RouteShape(sig)]
		if !ok {
			return fmt.Errorf("uncovered signature: %s: %s", p.Path, sig)
		}

		covered[op.Name] = true
	}

	return nil
}

func stripURLQuery(raw string) string { return strings.SplitN(raw, "?", 2)[0] }
