package retrievaleval

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v4"

	"lore/internal/search"
)

const (
	SuiteSchemaVersion  = 1
	ReportSchemaVersion = 1
	defaultResultLimit  = 20
)

var caseNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

type Suite struct {
	Version     int    `yaml:"version"`
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Corpus      string `yaml:"corpus"`
	Cases       []Case `yaml:"cases"`
}

type Case struct {
	ID           string         `yaml:"id"`
	Category     string         `yaml:"category"`
	Query        QuerySpec      `yaml:"query"`
	Relevant     []RelevantSpec `yaml:"relevant"`
	ForbiddenIDs []string       `yaml:"forbidden_ids,omitempty"`
}

type QuerySpec struct {
	Text          string              `yaml:"text" json:"text"`
	Scope         search.Scope        `yaml:"scope" json:"scope"`
	Kind          string              `yaml:"kind,omitempty" json:"kind,omitempty"`
	Tags          []string            `yaml:"tags,omitempty" json:"tags,omitempty"`
	Paths         []string            `yaml:"paths,omitempty" json:"paths,omitempty"`
	Limit         int                 `yaml:"limit,omitempty" json:"limit"`
	Matching      search.MatchingMode `yaml:"matching,omitempty" json:"matching"`
	Sensitivities []string            `yaml:"sensitivities" json:"sensitivities"`
}

type RelevantSpec struct {
	ID    string `yaml:"id" json:"id"`
	Grade int    `yaml:"grade" json:"grade"`
}

type LoadedSuite struct {
	Suite      Suite
	Path       string
	CorpusPath string
}

func LoadSuite(path string) (LoadedSuite, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return LoadedSuite{}, fmt.Errorf("resolve retrieval suite path: %w", err)
	}
	file, err := os.Open(absolute)
	if err != nil {
		return LoadedSuite{}, fmt.Errorf("open retrieval suite: %w", err)
	}
	defer file.Close()

	var suite Suite
	decoder := yaml.NewDecoder(file)
	decoder.KnownFields(true)
	if err := decoder.Decode(&suite); err != nil {
		return LoadedSuite{}, fmt.Errorf("parse retrieval suite: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return LoadedSuite{}, fmt.Errorf("parse retrieval suite: multiple YAML documents are not allowed")
		}
		return LoadedSuite{}, fmt.Errorf("parse retrieval suite: %w", err)
	}
	if err := suite.Validate(); err != nil {
		return LoadedSuite{}, err
	}

	directory := filepath.Dir(absolute)
	corpusPath, err := resolveCorpusPath(directory, suite.Corpus)
	if err != nil {
		return LoadedSuite{}, err
	}
	return LoadedSuite{Suite: suite, Path: absolute, CorpusPath: corpusPath}, nil
}

func (suite Suite) Validate() error {
	if suite.Version != SuiteSchemaVersion {
		return fmt.Errorf("retrieval suite version must equal %d", SuiteSchemaVersion)
	}
	if strings.TrimSpace(suite.Name) == "" {
		return fmt.Errorf("retrieval suite name is required")
	}
	if suite.Corpus == "" {
		return fmt.Errorf("retrieval suite corpus is required")
	}
	if filepath.IsAbs(suite.Corpus) || filepath.Clean(suite.Corpus) == "." || escapesPath(suite.Corpus) {
		return fmt.Errorf("retrieval suite corpus must be a relative child path")
	}
	if len(suite.Cases) == 0 {
		return fmt.Errorf("retrieval suite must contain at least one case")
	}
	seen := make(map[string]struct{}, len(suite.Cases))
	for index, testCase := range suite.Cases {
		if err := testCase.Validate(); err != nil {
			return fmt.Errorf("retrieval case %d: %w", index+1, err)
		}
		if _, exists := seen[testCase.ID]; exists {
			return fmt.Errorf("retrieval case ID %q is duplicated", testCase.ID)
		}
		seen[testCase.ID] = struct{}{}
	}
	return nil
}

func (testCase Case) Validate() error {
	if !caseNamePattern.MatchString(testCase.ID) {
		return fmt.Errorf("id must match %s", caseNamePattern)
	}
	if !caseNamePattern.MatchString(testCase.Category) {
		return fmt.Errorf("category must match %s", caseNamePattern)
	}
	if len(testCase.Relevant) == 0 {
		return fmt.Errorf("relevant must contain at least one document")
	}
	if len(testCase.Query.Sensitivities) == 0 {
		return fmt.Errorf("query sensitivities must be explicit and non-empty")
	}
	if _, err := testCase.Query.SearchQuery(search.BackendFilesystem); err != nil {
		return err
	}

	relevant := make(map[string]struct{}, len(testCase.Relevant))
	for _, expected := range testCase.Relevant {
		if strings.TrimSpace(expected.ID) == "" {
			return fmt.Errorf("relevant document ID must be non-empty")
		}
		if expected.Grade < 1 || expected.Grade > 3 {
			return fmt.Errorf("relevance grade for %q must be between 1 and 3", expected.ID)
		}
		if _, exists := relevant[expected.ID]; exists {
			return fmt.Errorf("relevant document ID %q is duplicated", expected.ID)
		}
		relevant[expected.ID] = struct{}{}
	}
	forbidden := make(map[string]struct{}, len(testCase.ForbiddenIDs))
	for _, id := range testCase.ForbiddenIDs {
		if strings.TrimSpace(id) == "" {
			return fmt.Errorf("forbidden document ID must be non-empty")
		}
		if _, exists := forbidden[id]; exists {
			return fmt.Errorf("forbidden document ID %q is duplicated", id)
		}
		if _, exists := relevant[id]; exists {
			return fmt.Errorf("document ID %q cannot be both relevant and forbidden", id)
		}
		forbidden[id] = struct{}{}
	}
	return nil
}

func (spec QuerySpec) SearchQuery(backend search.Backend) (search.Query, error) {
	access, err := search.NewAccessPolicy(spec.Sensitivities)
	if err != nil {
		return search.Query{}, fmt.Errorf("query sensitivity policy: %w", err)
	}
	limit := spec.Limit
	if limit == 0 {
		limit = defaultResultLimit
	}
	query := search.Query{
		Text:     spec.Text,
		Scope:    spec.Scope,
		Kind:     spec.Kind,
		Tags:     append([]string(nil), spec.Tags...),
		Paths:    append([]string(nil), spec.Paths...),
		Limit:    limit,
		Backend:  backend,
		Matching: spec.Matching,
		Access:   access,
	}
	if err := search.ValidateQuery(query); err != nil {
		return search.Query{}, fmt.Errorf("invalid query: %w", err)
	}
	return query, nil
}

func resolveCorpusPath(suiteDirectory, relative string) (string, error) {
	joined := filepath.Join(suiteDirectory, filepath.FromSlash(relative))
	resolved, err := filepath.EvalSymlinks(joined)
	if err != nil {
		return "", fmt.Errorf("resolve retrieval corpus: %w", err)
	}
	relativeToSuite, err := filepath.Rel(suiteDirectory, resolved)
	if err != nil || escapesPath(relativeToSuite) {
		return "", fmt.Errorf("retrieval corpus must remain beneath the suite directory")
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("inspect retrieval corpus: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("retrieval corpus is not a directory")
	}
	return resolved, nil
}

func escapesPath(path string) bool {
	clean := filepath.Clean(path)
	return clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator))
}
