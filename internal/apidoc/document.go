package apidoc

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var (
	documentLinkPattern   = regexp.MustCompile(`\[([^\]]+)\]\([^)]*\)`)
	documentRoutePattern  = regexp.MustCompile(`(?m)(GET|POST|PUT|PATCH|DELETE)\s+(?:https://api\.tracker\.yandex\.net)?(/v[23]/[^\s<\x60?]+)`)
	documentStatusPattern = regexp.MustCompile("(?:кодом|код)\\s+`?([2-5][0-9]{2})")
)

// Documents is a source-derived snapshot, independent of the SDK model registry.
// It retains contract facts and examples, not copies of the documentation prose.
type Documents struct {
	Version  int        `json:"version"`
	Revision string     `json:"revision"`
	Pages    []Document `json:"pages"`
}

// Document records the contract facts extracted from one official Markdown page.
type Document struct {
	Path       string            `json:"path"`
	Digest     string            `json:"digest"`
	SHA256     string            `json:"sha256"`
	Signatures []string          `json:"signatures,omitempty"`
	Fields     []DocumentField   `json:"fields,omitempty"`
	Examples   []DocumentExample `json:"examples,omitempty"`
}

// DocumentField identifies a table member and fingerprints its complete row.
type DocumentField struct {
	Section  string `json:"section"`
	Name     string `json:"name"`
	Type     string `json:"type,omitempty"`
	Required string `json:"required,omitempty"`
	Digest   string `json:"digest"`
	Line     int    `json:"line"`
	Note     string `json:"note,omitempty"`
}

// DocumentExample is an unmodified, valid JSON example from the official source.
type DocumentExample struct {
	Section string          `json:"section"`
	Status  int             `json:"status,omitempty"`
	Line    int             `json:"line"`
	Value   json.RawMessage `json:"value"`
}

type documentParser struct {
	document               Document
	lines                  []string
	major, group, required string
	status                 int
	cuts, columns          []string
}

// ExtractDocument parses both ordinary and Diplodoc multiline Markdown tables.
// Description changes remain visible through row digests even without a type change.
func ExtractDocument(path string, body []byte) (Document, error) {
	p := documentParser{document: Document{Path: path, Digest: ContentDigest(body)}, lines: strings.Split(string(body), "\n")}
	for _, match := range documentRoutePattern.FindAllStringSubmatch(string(body), -1) {
		signature := match[1] + " " + strings.TrimRight(match[2], "/")
		if !containsString(p.document.Signatures, signature) {
			p.document.Signatures = append(p.document.Signatures, signature)
		}
	}

	for i := 0; i < len(p.lines); i++ {
		if err := p.consume(&i); err != nil {
			return p.document, err
		}
	}

	return p.document, nil
}

func (p *documentParser) consume(index *int) error {
	line := strings.TrimSpace(strings.TrimLeft(p.lines[*index], "> "))
	p.updateSection(line)

	if strings.HasPrefix(line, "```") {
		p.readExample(index)
		return nil
	}

	if line == "#|" || line == "|#" {
		p.columns = nil
		return nil
	}

	start := *index + 1

	if strings.HasPrefix(line, "||") {
		parts := []string{line}
		for !strings.HasSuffix(line, "||") || len(parts) == 1 && len(line) == 2 {
			*index++
			if *index >= len(p.lines) {
				return fmt.Errorf("%s:%d: unterminated table row", p.document.Path, start)
			}

			line = strings.TrimSpace(p.lines[*index])
			parts = append(parts, line)
		}

		line = strings.Join(parts, "\n")
		line = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(line, "||"), "||"))
	}

	if !strings.Contains(line, "|") || strings.HasPrefix(line, "<!--") {
		if line == "" {
			p.columns = nil
		}

		return nil
	}

	p.addRow(line, start)

	return nil
}

func (p *documentParser) updateSection(line string) {
	if strings.HasPrefix(line, "## ") {
		p.major, p.group, p.required, p.status = line[3:], "", "", 0
		p.cuts, p.columns = nil, nil
	}

	if strings.HasPrefix(line, "{% cut ") {
		label := strings.TrimSuffix(strings.TrimPrefix(line, "{% cut "), " %}")
		p.cuts = append(p.cuts, strings.Trim(label, "\""))
		p.columns, p.group, p.required = nil, "", ""
	}

	if strings.HasPrefix(line, "{% endcut") && len(p.cuts) != 0 {
		p.cuts = p.cuts[:len(p.cuts)-1]
		p.columns, p.group, p.required = nil, "", ""
	}

	if strings.HasPrefix(line, "**") && !strings.Contains(line, "Documentation Index:") {
		p.group = plainDocumentText(line)
		if strings.Contains(p.group, "Обязательные") {
			p.required = "required"
		} else if strings.Contains(p.group, "Дополнительные") {
			p.required = "optional"
		}
	}

	if match := documentStatusPattern.FindStringSubmatch(line); match != nil {
		p.status, _ = strconv.Atoi(match[1])
	}
}

func (p *documentParser) section() string {
	return strings.Join(append(append([]string{p.major}, p.cuts...), p.group), " / ")
}

func (p *documentParser) readExample(index *int) {
	start := *index + 1

	var block []string

	for *index++; *index < len(p.lines); *index++ {
		if strings.HasPrefix(strings.TrimSpace(strings.TrimLeft(p.lines[*index], "> ")), "```") {
			break
		}

		block = append(block, strings.TrimLeft(p.lines[*index], "> "))
	}

	if example := documentJSONExample(strings.Join(block, "\n")); example != nil {
		p.document.Examples = append(p.document.Examples, DocumentExample{Section: p.section(), Status: p.status, Line: start + 1, Value: example})
	}
}

func (p *documentParser) addRow(line string, start int) {
	cells := splitDocumentCells(line)
	if len(cells) < 2 {
		return
	}

	if isDocumentHeader(cells) {
		p.columns = cells
		return
	}

	if len(p.columns) == 0 || strings.Trim(cells[0], "-: ") == "" {
		return
	}

	canonical := revisionQueryPattern.ReplaceAllString(strings.Join(strings.Fields(line), " "), "")

	field := DocumentField{Section: p.section(), Name: cells[0], Required: p.required, Line: start, Digest: fmt.Sprintf("%x", sha256.Sum256([]byte(canonical)))}
	if len(cells) != len(p.columns) {
		field.Note = fmt.Sprintf("source table has %d cells, expected %d", len(cells), len(p.columns))
		if len(cells) > len(p.columns) && strings.Contains(strings.ToLower(p.columns[len(p.columns)-1]), "тип") {
			field.Type = cells[len(cells)-1]
		}
	} else {
		for i, column := range p.columns {
			if strings.Contains(strings.ToLower(column), "тип") {
				field.Type = cells[i]
			}

			if strings.Contains(strings.ToLower(column), "обязател") {
				field.Required = cells[i]
			}
		}
	}

	p.document.Fields = append(p.document.Fields, field)
}

func documentJSONExample(block string) json.RawMessage {
	if json.Valid([]byte(block)) {
		var compact bytes.Buffer
		if json.Compact(&compact, []byte(block)) == nil {
			return append(json.RawMessage(nil), compact.Bytes()...)
		}
	}

	lines := strings.Split(block, "\n")
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "{") && !strings.HasPrefix(trimmed, "[") {
			continue
		}

		value := []byte(strings.Join(lines[index:], "\n"))
		if json.Valid(value) {
			var compact bytes.Buffer
			if json.Compact(&compact, value) == nil {
				return append(json.RawMessage(nil), compact.Bytes()...)
			}
		}
		// A malformed outer value is not a separate valid nested example.
		return nil
	}

	return nil
}

func splitDocumentCells(line string) []string {
	line = strings.TrimSpace(line)
	if strings.HasPrefix(line, "|") {
		line = strings.TrimSuffix(strings.TrimPrefix(line, "|"), "|")
	}

	var cells []string

	start, quoted, escaped := 0, false, false
	for index, ch := range line {
		if escaped {
			escaped = false
			continue
		}

		if ch == '\\' {
			escaped = true
		} else if ch == '`' {
			quoted = !quoted
		} else if ch == '|' && !quoted {
			cells = append(cells, plainDocumentText(line[start:index]))
			start = index + 1
		}
	}

	return append(cells, plainDocumentText(line[start:]))
}

func plainDocumentText(value string) string {
	value = documentLinkPattern.ReplaceAllString(value, "$1")
	value = strings.NewReplacer("`", "", "**", "", "\\|", "|").Replace(value)

	return strings.Join(strings.Fields(value), " ")
}

func isDocumentHeader(cells []string) bool {
	first := strings.ToLower(cells[0])
	return strings.Contains(first, "параметр") || first == "поле" || first == "название" || first == "parameter"
}

func containsString(values []string, value string) bool {
	for _, existing := range values {
		if value == existing {
			return true
		}
	}

	return false
}
