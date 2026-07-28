package config

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode"

	"go.yaml.in/yaml/v4"
)

const (
	DefaultMaxBytes = int64(4 * 1024 * 1024)
	MaxMaxBytes     = int64(64 * 1024 * 1024)
)

type Config struct {
	Version int           `yaml:"version"`
	Git     GitConfig     `yaml:"git"`
	Capture CaptureConfig `yaml:"capture"`
}

type GitConfig struct {
	AutoCommitCaptures   bool   `yaml:"auto_commit_captures"`
	AutoPushCaptures     bool   `yaml:"auto_push_captures"`
	AutoPushTransactions bool   `yaml:"auto_push_transactions"`
	Remote               string `yaml:"remote"`
	RequirePush          bool   `yaml:"require_push"`
}

type CaptureConfig struct {
	MaxBytes int64 `yaml:"max_bytes"`
}

func Defaults() Config {
	return Config{
		Version: 1,
		Git: GitConfig{
			AutoCommitCaptures:   true,
			AutoPushCaptures:     false,
			AutoPushTransactions: false,
			Remote:               "origin",
			RequirePush:          false,
		},
		Capture: CaptureConfig{
			MaxBytes: DefaultMaxBytes,
		},
	}
}

// Parse applies v0.1 defaults and rejects unknown YAML fields.
func Parse(data []byte) (Config, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return Config{}, fmt.Errorf("parse lore.yaml: %w", err)
	}
	if !hasTopLevelKey(&root, "version") {
		return Config{}, fmt.Errorf("lore.yaml version is required")
	}
	cfg := Defaults()
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("parse lore.yaml: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return Config{}, fmt.Errorf("parse lore.yaml: multiple YAML documents are not allowed")
		}
		return Config{}, fmt.Errorf("parse lore.yaml: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func hasTopLevelKey(root *yaml.Node, key string) bool {
	if root == nil || len(root.Content) == 0 {
		return false
	}
	mapping := root.Content[0]
	if mapping.Kind != yaml.MappingNode {
		return false
	}
	for index := 0; index+1 < len(mapping.Content); index += 2 {
		if mapping.Content[index].Value == key {
			return true
		}
	}
	return false
}

func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read lore.yaml: %w", err)
	}
	return Parse(data)
}

func (c Config) Validate() error {
	if c.Version != 1 {
		return fmt.Errorf("lore.yaml version must equal 1")
	}
	if c.Git.Remote == "" {
		return fmt.Errorf("git.remote must not be empty")
	}
	if strings.HasPrefix(c.Git.Remote, "-") || strings.ContainsRune(c.Git.Remote, '\x00') ||
		strings.IndexFunc(c.Git.Remote, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return fmt.Errorf("git.remote must be a Git remote name without leading dashes, whitespace, NUL bytes, or control characters")
	}
	if c.Capture.MaxBytes <= 0 {
		return fmt.Errorf("capture.max_bytes must be positive")
	}
	if c.Capture.MaxBytes > MaxMaxBytes {
		return fmt.Errorf("capture.max_bytes must not exceed %d bytes", MaxMaxBytes)
	}
	return nil
}
