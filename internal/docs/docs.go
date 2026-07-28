package docs

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/oklog/ulid/v2"
	"go.yaml.in/yaml/v4"
)

type Type string

const (
	TypePage   Type = "page"
	TypeSource Type = "source"
)

var (
	tokenPattern  = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)
	pageIDPattern = regexp.MustCompile(`^page_[a-z0-9][a-z0-9_]*$`)
	hashPattern   = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	datePattern   = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
)

type Source struct {
	ID             string          `yaml:"id"`
	Kind           string          `yaml:"kind"`
	CapturedAt     TimestampString `yaml:"captured_at"`
	Origin         string          `yaml:"origin"`
	OriginRef      string          `yaml:"origin_ref,omitempty"`
	RawSHA256      string          `yaml:"raw_sha256"`
	Sensitivity    string          `yaml:"sensitivity"`
	Tags           []string        `yaml:"tags,omitempty"`
	IntegratedAt   TimestampString `yaml:"integrated_at,omitempty"`
	IntegratedInto []string        `yaml:"integrated_into,omitempty"`
}

type Page struct {
	ID          string          `yaml:"id"`
	Title       string          `yaml:"title"`
	Kind        string          `yaml:"kind"`
	Aliases     []string        `yaml:"aliases,omitempty"`
	Created     TimestampString `yaml:"created"`
	Updated     TimestampString `yaml:"updated"`
	Status      string          `yaml:"status"`
	Sensitivity string          `yaml:"sensitivity"`
	Tags        []string        `yaml:"tags,omitempty"`
}

// TimestampString accepts both YAML string and timestamp scalars while
// retaining their exact textual value for the v0.1 metadata validators.
type TimestampString string

func (s *TimestampString) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.ScalarNode || (node.Tag != "!!str" && node.Tag != "!!timestamp") {
		return fmt.Errorf("expected a string or timestamp scalar")
	}
	*s = TimestampString(node.Value)
	return nil
}

func (s TimestampString) MarshalYAML() (any, error) {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: string(s)}, nil
}

type Document struct {
	Path       string
	Type       Type
	Data       []byte
	Body       []byte
	BodyOffset int
	Source     *Source
	Page       *Page
}

func (d *Document) ID() string {
	if d.Source != nil {
		return d.Source.ID
	}
	if d.Page != nil {
		return d.Page.ID
	}
	return ""
}

func (d *Document) Kind() string {
	if d.Source != nil {
		return d.Source.Kind
	}
	if d.Page != nil {
		return d.Page.Kind
	}
	return ""
}

func (d *Document) Title() string {
	if d.Page != nil {
		return d.Page.Title
	}
	return ""
}

func (d *Document) Aliases() []string {
	if d.Page != nil {
		return d.Page.Aliases
	}
	return nil
}

func (d *Document) Tags() []string {
	if d.Source != nil {
		return d.Source.Tags
	}
	if d.Page != nil {
		return d.Page.Tags
	}
	return nil
}

func Parse(path string, data []byte) (*Document, error) {
	if !utf8.Valid(data) {
		return nil, fmt.Errorf("managed Markdown is not valid UTF-8")
	}
	frontmatter, body, offset, err := splitFrontmatter(data)
	if err != nil {
		return nil, err
	}
	clean := filepath.ToSlash(filepath.Clean(path))
	doc := &Document{Path: clean, Data: data, Body: body, BodyOffset: offset}
	switch {
	case strings.HasPrefix(clean, "sources/"):
		doc.Type = TypeSource
		var source Source
		if err := yaml.Unmarshal(frontmatter, &source); err != nil {
			return nil, fmt.Errorf("parse source frontmatter: %w", err)
		}
		doc.Source = &source
	case strings.HasPrefix(clean, "pages/"):
		doc.Type = TypePage
		var page Page
		if err := yaml.Unmarshal(frontmatter, &page); err != nil {
			return nil, fmt.Errorf("parse page frontmatter: %w", err)
		}
		doc.Page = &page
	default:
		return nil, fmt.Errorf("managed document must be under pages/ or sources/")
	}
	return doc, nil
}

func splitFrontmatter(data []byte) (frontmatter, body []byte, bodyOffset int, err error) {
	first, next, ok := nextLine(data, 0)
	if !ok || !bytes.Equal(first, []byte("---")) {
		return nil, nil, 0, fmt.Errorf("missing opening YAML frontmatter delimiter")
	}
	for pos := next; pos <= len(data); {
		line, after, exists := nextLine(data, pos)
		if !exists {
			break
		}
		if bytes.Equal(line, []byte("---")) {
			if after == 0 || data[after-1] != '\n' {
				return nil, nil, 0, fmt.Errorf("closing YAML frontmatter delimiter must end with a newline")
			}
			return data[next:pos], data[after:], after, nil
		}
		if after <= pos {
			break
		}
		pos = after
	}
	return nil, nil, 0, fmt.Errorf("missing closing YAML frontmatter delimiter")
}

// nextLine returns a line without its LF or optional CR and the offset after LF.
// A final non-newline-terminated line is also returned.
func nextLine(data []byte, start int) ([]byte, int, bool) {
	if start < 0 || start >= len(data) {
		return nil, start, false
	}
	relative := bytes.IndexByte(data[start:], '\n')
	if relative < 0 {
		line := data[start:]
		if len(line) > 0 && line[len(line)-1] == '\r' {
			line = line[:len(line)-1]
		}
		return line, len(data), true
	}
	end := start + relative
	line := data[start:end]
	if len(line) > 0 && line[len(line)-1] == '\r' {
		line = line[:len(line)-1]
	}
	return line, end + 1, true
}

func Validate(doc *Document) []error {
	switch doc.Type {
	case TypeSource:
		return ValidateSource(doc.Source)
	case TypePage:
		return ValidatePage(doc.Page)
	default:
		return []error{fmt.Errorf("unknown document type %q", doc.Type)}
	}
}

func ValidateSource(source *Source) []error {
	var errs []error
	if source == nil {
		return []error{fmt.Errorf("source metadata is missing")}
	}
	if err := ValidateSourceID(source.ID); err != nil {
		errs = append(errs, err)
	}
	if !ValidToken(source.Kind) {
		errs = append(errs, fmt.Errorf("kind must match %s", tokenPattern))
	}
	if captured, err := time.Parse(time.RFC3339, string(source.CapturedAt)); err != nil {
		errs = append(errs, fmt.Errorf("captured_at must be RFC 3339"))
	} else if _, offset := captured.Zone(); offset != 0 {
		errs = append(errs, fmt.Errorf("captured_at must use UTC"))
	}
	if !ValidToken(source.Origin) {
		errs = append(errs, fmt.Errorf("origin must match %s", tokenPattern))
	}
	if !hashPattern.MatchString(source.RawSHA256) {
		errs = append(errs, fmt.Errorf("raw_sha256 must be a lowercase SHA-256 value"))
	}
	if !ValidSensitivity(source.Sensitivity) {
		errs = append(errs, fmt.Errorf("sensitivity must be normal, sensitive, or local-only"))
	}
	errs = append(errs, validateStrings("tags", source.Tags)...)
	if source.IntegratedAt != "" {
		if integrated, err := time.Parse(time.RFC3339, string(source.IntegratedAt)); err != nil {
			errs = append(errs, fmt.Errorf("integrated_at must be RFC 3339"))
		} else if _, offset := integrated.Zone(); offset != 0 {
			errs = append(errs, fmt.Errorf("integrated_at must use UTC"))
		}
	}
	seenPageIDs := make(map[string]struct{}, len(source.IntegratedInto))
	for _, pageID := range source.IntegratedInto {
		if err := ValidatePageID(pageID); err != nil {
			errs = append(errs, fmt.Errorf("integrated_into contains invalid page ID %q", pageID))
		}
		if _, exists := seenPageIDs[pageID]; exists {
			errs = append(errs, fmt.Errorf("integrated_into must contain unique page IDs"))
			break
		}
		seenPageIDs[pageID] = struct{}{}
	}
	return errs
}

func ValidatePage(page *Page) []error {
	var errs []error
	if page == nil {
		return []error{fmt.Errorf("page metadata is missing")}
	}
	if err := ValidatePageID(page.ID); err != nil {
		errs = append(errs, err)
	}
	if strings.TrimSpace(page.Title) == "" {
		errs = append(errs, fmt.Errorf("title must not be empty"))
	}
	if !ValidToken(page.Kind) {
		errs = append(errs, fmt.Errorf("kind must match %s", tokenPattern))
	}
	created, createdErr := parseDate(string(page.Created))
	if createdErr != nil {
		errs = append(errs, fmt.Errorf("created must be an ISO YYYY-MM-DD date"))
	}
	updated, updatedErr := parseDate(string(page.Updated))
	if updatedErr != nil {
		errs = append(errs, fmt.Errorf("updated must be an ISO YYYY-MM-DD date"))
	}
	if createdErr == nil && updatedErr == nil && updated.Before(created) {
		errs = append(errs, fmt.Errorf("updated must not precede created"))
	}
	switch page.Status {
	case "active", "inactive", "archived", "superseded":
	default:
		errs = append(errs, fmt.Errorf("status must be active, inactive, archived, or superseded"))
	}
	if !ValidSensitivity(page.Sensitivity) {
		errs = append(errs, fmt.Errorf("sensitivity must be normal, sensitive, or local-only"))
	}
	errs = append(errs, validateStrings("aliases", page.Aliases)...)
	errs = append(errs, validateStrings("tags", page.Tags)...)
	return errs
}

func ValidateSourceID(value string) error {
	if !strings.HasPrefix(value, "src_") || len(value) != 30 {
		return fmt.Errorf("id must be src_ followed by a 26-character canonical ULID")
	}
	raw := strings.TrimPrefix(value, "src_")
	parsed, err := ulid.ParseStrict(raw)
	if err != nil || parsed.String() != raw {
		return fmt.Errorf("id must be src_ followed by a 26-character canonical ULID")
	}
	return nil
}

func ValidatePageID(value string) error {
	if !pageIDPattern.MatchString(value) {
		return fmt.Errorf("id must match %s", pageIDPattern)
	}
	return nil
}

func ValidToken(value string) bool {
	return tokenPattern.MatchString(value)
}

func ValidSensitivity(value string) bool {
	switch value {
	case "normal", "sensitive", "local-only":
		return true
	default:
		return false
	}
}

func SHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func Revision(data []byte) string {
	return SHA256(data)
}

func MarshalSource(source Source, body []byte) ([]byte, error) {
	if errs := ValidateSource(&source); len(errs) > 0 {
		return nil, errs[0]
	}
	frontmatter, err := yaml.Marshal(source)
	if err != nil {
		return nil, fmt.Errorf("marshal source frontmatter: %w", err)
	}
	data := make([]byte, 0, len(frontmatter)+len(body)+8)
	data = append(data, "---\n"...)
	data = append(data, frontmatter...)
	data = append(data, "---\n"...)
	data = append(data, body...)
	return data, nil
}

// MarkSourceIntegrated updates only the integration fields in source
// frontmatter. Unknown frontmatter fields survive YAML re-serialization and
// the source body bytes are appended without modification.
func MarkSourceIntegrated(path string, data []byte, integratedAt time.Time, pageIDs []string) ([]byte, error) {
	document, err := Parse(path, data)
	if err != nil {
		return nil, err
	}
	if document.Source == nil {
		return nil, fmt.Errorf("document is not a source")
	}
	if errs := ValidateSource(document.Source); len(errs) > 0 {
		return nil, errs[0]
	}
	if SHA256(document.Body) != document.Source.RawSHA256 {
		return nil, fmt.Errorf("source body SHA-256 does not match raw_sha256")
	}
	union := make(map[string]struct{}, len(document.Source.IntegratedInto)+len(pageIDs))
	for _, pageID := range document.Source.IntegratedInto {
		union[pageID] = struct{}{}
	}
	for _, pageID := range pageIDs {
		if err := ValidatePageID(pageID); err != nil {
			return nil, fmt.Errorf("invalid integrated page ID %q", pageID)
		}
		union[pageID] = struct{}{}
	}
	integratedInto := make([]string, 0, len(union))
	for pageID := range union {
		integratedInto = append(integratedInto, pageID)
	}
	sort.Strings(integratedInto)

	frontmatter, _, _, err := splitFrontmatter(data)
	if err != nil {
		return nil, err
	}
	var root yaml.Node
	if err := yaml.Unmarshal(frontmatter, &root); err != nil {
		return nil, fmt.Errorf("parse source frontmatter: %w", err)
	}
	if len(root.Content) != 1 || root.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("source frontmatter must be a YAML mapping")
	}
	mapping := root.Content[0]
	setMappingValue(mapping, "integrated_at", &yaml.Node{
		Kind:  yaml.ScalarNode,
		Tag:   "!!str",
		Value: integratedAt.UTC().Format(time.RFC3339Nano),
	})
	sequence := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	for _, pageID := range integratedInto {
		sequence.Content = append(sequence.Content, &yaml.Node{
			Kind:  yaml.ScalarNode,
			Tag:   "!!str",
			Value: pageID,
		})
	}
	setMappingValue(mapping, "integrated_into", sequence)
	encoded, err := yaml.Marshal(&root)
	if err != nil {
		return nil, fmt.Errorf("marshal source frontmatter: %w", err)
	}
	result := make([]byte, 0, len(encoded)+len(document.Body)+8)
	result = append(result, "---\n"...)
	result = append(result, encoded...)
	result = append(result, "---\n"...)
	result = append(result, document.Body...)
	updated, err := Parse(path, result)
	if err != nil {
		return nil, fmt.Errorf("validate updated source: %w", err)
	}
	if SHA256(updated.Body) != updated.Source.RawSHA256 || !bytes.Equal(updated.Body, document.Body) {
		return nil, fmt.Errorf("source body changed while updating integration metadata")
	}
	return result, nil
}

func setMappingValue(mapping *yaml.Node, key string, value *yaml.Node) {
	for index := 0; index+1 < len(mapping.Content); index += 2 {
		if mapping.Content[index].Value == key {
			mapping.Content[index+1] = value
			return
		}
	}
	mapping.Content = append(mapping.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		value,
	)
}

func parseDate(value string) (time.Time, error) {
	if !datePattern.MatchString(value) {
		return time.Time{}, fmt.Errorf("invalid date")
	}
	parsed, err := time.Parse("2006-01-02", value)
	if err != nil || parsed.Format("2006-01-02") != value {
		return time.Time{}, fmt.Errorf("invalid date")
	}
	return parsed, nil
}

func validateStrings(field string, values []string) []error {
	var errs []error
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			errs = append(errs, fmt.Errorf("%s must contain only non-empty strings", field))
			break
		}
	}
	return errs
}
