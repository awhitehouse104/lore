package serverconfig

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"

	"lore/internal/auth"
	"lore/internal/repository"

	"go.yaml.in/yaml/v4"
)

const (
	DefaultPath                        = "/etc/lore/mcp.yaml"
	DefaultListen                      = "127.0.0.1:8787"
	DefaultEndpoint                    = "/mcp"
	DefaultRequestMaxBytes       int64 = 8 * 1024 * 1024
	DefaultResponseMaxBytes      int64 = 8 * 1024 * 1024
	DefaultMaxConcurrentRequests       = 8
	DefaultRequestTimeout              = 60 * time.Second
	DefaultShutdownTimeout             = 15 * time.Second
	MaximumConfigBytes           int64 = 1 * 1024 * 1024
	MaximumRequestBytes          int64 = 64 * 1024 * 1024
	MaximumResponseBytes         int64 = 64 * 1024 * 1024
	MaximumConcurrentRequests          = 64
)

type Duration time.Duration

func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.ScalarNode || node.Tag != "!!str" {
		return fmt.Errorf("duration must be a quoted or plain duration string")
	}
	value, err := time.ParseDuration(node.Value)
	if err != nil {
		return fmt.Errorf("invalid duration")
	}
	*d = Duration(value)
	return nil
}

func (d Duration) Value() time.Duration {
	return time.Duration(d)
}

type Config struct {
	Version   int             `yaml:"version"`
	Repo      string          `yaml:"repo"`
	Listen    string          `yaml:"listen"`
	Endpoint  string          `yaml:"endpoint"`
	Transport TransportConfig `yaml:"transport"`
	Auth      AuthConfig      `yaml:"auth"`
	Logging   LoggingConfig   `yaml:"logging"`

	BearerPrincipals  []auth.BearerPrincipal `yaml:"-"`
	NormalizedOrigins []string               `yaml:"-"`
	listenExplicit    bool
	listenIP          net.IP
}

type TransportConfig struct {
	RequestMaxBytes           int64    `yaml:"request_max_bytes"`
	ResponseMaxBytes          int64    `yaml:"response_max_bytes"`
	MaxConcurrentRequests     int      `yaml:"max_concurrent_requests"`
	RequestTimeout            Duration `yaml:"request_timeout"`
	ShutdownTimeout           Duration `yaml:"shutdown_timeout"`
	AllowedOrigins            []string `yaml:"allowed_origins"`
	TrustForwardedHeaders     bool     `yaml:"trust_forwarded_headers"`
	AllowPlaintextNonLoopback bool     `yaml:"allow_plaintext_non_loopback"`
}

type AuthConfig struct {
	Tokens []TokenConfig `yaml:"tokens"`
}

type TokenConfig struct {
	Name          string            `yaml:"name"`
	TokenFile     string            `yaml:"token_file"`
	Permissions   []auth.Permission `yaml:"permissions"`
	Sensitivities []string          `yaml:"sensitivities"`
}

type LoggingConfig struct {
	Format      string `yaml:"format"`
	Level       string `yaml:"level"`
	Destination string `yaml:"destination"`
}

func Defaults() Config {
	return Config{
		Version:  1,
		Listen:   DefaultListen,
		Endpoint: DefaultEndpoint,
		Transport: TransportConfig{
			RequestMaxBytes:           DefaultRequestMaxBytes,
			ResponseMaxBytes:          DefaultResponseMaxBytes,
			MaxConcurrentRequests:     DefaultMaxConcurrentRequests,
			RequestTimeout:            Duration(DefaultRequestTimeout),
			ShutdownTimeout:           Duration(DefaultShutdownTimeout),
			AllowedOrigins:            []string{},
			TrustForwardedHeaders:     false,
			AllowPlaintextNonLoopback: false,
		},
		Auth: AuthConfig{Tokens: []TokenConfig{}},
		Logging: LoggingConfig{
			Format:      "json",
			Level:       "info",
			Destination: "stderr",
		},
		BearerPrincipals:  []auth.BearerPrincipal{},
		NormalizedOrigins: []string{},
	}
}

func Load(configPath string) (Config, error) {
	if configPath == "" {
		configPath = DefaultPath
	}
	data, err := readRegularFile(configPath, MaximumConfigBytes, false)
	if err != nil {
		return Config{}, fmt.Errorf("read MCP server configuration: %w", err)
	}
	return Parse(data)
}

func Parse(data []byte) (Config, error) {
	if int64(len(data)) > MaximumConfigBytes {
		return Config{}, fmt.Errorf("MCP server configuration exceeds %d bytes", MaximumConfigBytes)
	}
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return Config{}, fmt.Errorf("parse MCP server configuration: %w", err)
	}
	if err := rejectDuplicateKeys(&root); err != nil {
		return Config{}, fmt.Errorf("parse MCP server configuration: %w", err)
	}
	if !hasTopLevelKey(&root, "version") {
		return Config{}, fmt.Errorf("MCP server configuration version is required")
	}
	if !hasTopLevelKey(&root, "repo") {
		return Config{}, fmt.Errorf("MCP server repository path is required")
	}
	config := Defaults()
	config.listenExplicit = hasTopLevelKey(&root, "listen")
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&config); err != nil {
		return Config{}, fmt.Errorf("parse MCP server configuration: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return Config{}, fmt.Errorf("MCP server configuration must contain one YAML document")
		}
		return Config{}, fmt.Errorf("parse MCP server configuration: %w", err)
	}
	if err := config.validateAndLoad(); err != nil {
		return Config{}, err
	}
	return config, nil
}

func (c Config) IsLoopback() bool {
	return c.listenIP != nil && c.listenIP.IsLoopback()
}

func (c *Config) validateAndLoad() error {
	if c.Version != 1 {
		return fmt.Errorf("MCP server configuration version must equal 1")
	}
	repo, err := repository.Open(c.Repo)
	if err != nil {
		return fmt.Errorf("validate MCP repository: %w", err)
	}
	c.Repo = repo.Root
	if err := c.validateListen(); err != nil {
		return err
	}
	if err := validateEndpoint(c.Endpoint); err != nil {
		return err
	}
	if c.Transport.RequestMaxBytes < 1024 || c.Transport.RequestMaxBytes > MaximumRequestBytes {
		return fmt.Errorf("transport.request_max_bytes must be between 1024 and %d", MaximumRequestBytes)
	}
	if c.Transport.ResponseMaxBytes < 1024 || c.Transport.ResponseMaxBytes > MaximumResponseBytes {
		return fmt.Errorf("transport.response_max_bytes must be between 1024 and %d", MaximumResponseBytes)
	}
	if c.Transport.MaxConcurrentRequests < 1 || c.Transport.MaxConcurrentRequests > MaximumConcurrentRequests {
		return fmt.Errorf("transport.max_concurrent_requests must be between 1 and %d", MaximumConcurrentRequests)
	}
	if value := c.Transport.RequestTimeout.Value(); value < time.Second || value > 5*time.Minute {
		return fmt.Errorf("transport.request_timeout must be between 1s and 5m")
	}
	if value := c.Transport.ShutdownTimeout.Value(); value < time.Second || value > 2*time.Minute {
		return fmt.Errorf("transport.shutdown_timeout must be between 1s and 2m")
	}
	if c.Transport.TrustForwardedHeaders {
		return fmt.Errorf("transport.trust_forwarded_headers must remain false in v0.4")
	}
	origins := make([]string, 0, len(c.Transport.AllowedOrigins))
	seenOrigins := make(map[string]struct{}, len(c.Transport.AllowedOrigins))
	for _, value := range c.Transport.AllowedOrigins {
		normalized, err := NormalizeOrigin(value)
		if err != nil {
			return fmt.Errorf("transport.allowed_origins contains an invalid exact origin")
		}
		if _, exists := seenOrigins[normalized]; exists {
			return fmt.Errorf("transport.allowed_origins contains a duplicate origin")
		}
		seenOrigins[normalized] = struct{}{}
		origins = append(origins, normalized)
	}
	c.NormalizedOrigins = origins
	if c.Logging.Format != "json" && c.Logging.Format != "text" {
		return fmt.Errorf("logging.format must be json or text")
	}
	switch c.Logging.Level {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("logging.level must be debug, info, warn, or error")
	}
	if c.Logging.Destination != "stderr" {
		return fmt.Errorf("logging.destination must be stderr")
	}
	if len(c.Auth.Tokens) == 0 {
		return fmt.Errorf("auth.tokens must contain at least one bearer principal")
	}
	names := make(map[string]struct{}, len(c.Auth.Tokens))
	digests := make(map[auth.TokenDigest]string, len(c.Auth.Tokens))
	c.BearerPrincipals = make([]auth.BearerPrincipal, 0, len(c.Auth.Tokens))
	for _, token := range c.Auth.Tokens {
		if _, exists := names[token.Name]; exists {
			return fmt.Errorf("auth.tokens contains duplicate principal name %q", token.Name)
		}
		names[token.Name] = struct{}{}
		if err := rejectDuplicatePermissions(token.Permissions); err != nil {
			return fmt.Errorf("principal %q: %w", token.Name, err)
		}
		if err := rejectDuplicateSensitivities(token.Sensitivities); err != nil {
			return fmt.Errorf("principal %q: %w", token.Name, err)
		}
		principal, err := auth.NewPrincipal(token.Name, auth.TransportHTTP, token.Permissions, token.Sensitivities)
		if err != nil {
			return fmt.Errorf("principal %q is invalid: %w", token.Name, err)
		}
		if !filepath.IsAbs(token.TokenFile) || filepath.Clean(token.TokenFile) != token.TokenFile {
			return fmt.Errorf("token file for principal %q must be an absolute clean path", token.Name)
		}
		decoded, err := loadTokenFile(token.TokenFile)
		if err != nil {
			return fmt.Errorf("token file for principal %q is invalid", token.Name)
		}
		digest := auth.DigestBearerToken(decoded)
		if other, exists := digests[digest]; exists {
			return fmt.Errorf("principals %q and %q use the same bearer token", other, token.Name)
		}
		digests[digest] = token.Name
		c.BearerPrincipals = append(c.BearerPrincipals, auth.BearerPrincipal{
			Principal: principal,
			Digest:    digest,
		})
	}
	if !c.IsLoopback() {
		if !c.listenExplicit {
			return fmt.Errorf("a non-loopback listen address must be explicit")
		}
		if !c.Transport.AllowPlaintextNonLoopback {
			return fmt.Errorf("non-loopback plaintext serving requires transport.allow_plaintext_non_loopback: true")
		}
	}
	return nil
}

func (c *Config) validateListen() error {
	host, portText, err := net.SplitHostPort(c.Listen)
	if err != nil || host == "" {
		return fmt.Errorf("listen must be an explicit IP address and port")
	}
	ip := net.ParseIP(host)
	if ip == nil || ip.IsUnspecified() {
		return fmt.Errorf("listen must not use a hostname or unspecified address")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("listen port must be between 1 and 65535")
	}
	c.listenIP = append(net.IP(nil), ip...)
	return nil
}

func validateEndpoint(endpoint string) error {
	if endpoint == "" || !strings.HasPrefix(endpoint, "/") || endpoint == "/" ||
		path.Clean(endpoint) != endpoint || strings.ContainsAny(endpoint, "?#*{}\\%") ||
		endpoint == "/health/live" || endpoint == "/health/ready" {
		return fmt.Errorf("endpoint must be an exact absolute path without traversal, query, fragment, wildcard, escape, or health collision")
	}
	return nil
}

func NormalizeOrigin(value string) (string, error) {
	if value == "" || strings.TrimSpace(value) != value || strings.ContainsAny(value, "\r\n") {
		return "", fmt.Errorf("invalid origin")
	}
	parsed, err := url.ParseRequestURI(value)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" ||
		(parsed.Path != "" && parsed.Path != "/") {
		return "", fmt.Errorf("invalid origin")
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "" || strings.IndexFunc(host, func(r rune) bool {
		return r > unicode.MaxASCII || unicode.IsSpace(r) || unicode.IsControl(r)
	}) >= 0 {
		return "", fmt.Errorf("invalid origin")
	}
	if ip := net.ParseIP(host); ip != nil {
		host = ip.String()
	}
	port := parsed.Port()
	if port == "" {
		if parsed.Scheme == "http" {
			port = "80"
		} else {
			port = "443"
		}
	}
	number, err := strconv.Atoi(port)
	if err != nil || number < 1 || number > 65535 {
		return "", fmt.Errorf("invalid origin")
	}
	return strings.ToLower(parsed.Scheme) + "://" + net.JoinHostPort(host, strconv.Itoa(number)), nil
}

func loadTokenFile(filePath string) ([]byte, error) {
	data, err := readRegularFile(filePath, auth.MaximumAuthorizationHeaderBytes+2, true)
	if err != nil {
		return nil, err
	}
	if bytes.HasSuffix(data, []byte("\n")) {
		data = data[:len(data)-1]
		if bytes.HasSuffix(data, []byte("\r")) {
			data = data[:len(data)-1]
		}
	}
	if bytes.ContainsAny(data, "\r\n") {
		return nil, fmt.Errorf("token file must contain one token")
	}
	return auth.DecodeBearerToken(string(data))
}

func readRegularFile(filePath string, maximum int64, protected bool) ([]byte, error) {
	info, err := os.Lstat(filePath)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() > maximum {
		return nil, fmt.Errorf("path must be a bounded regular file")
	}
	file, err := os.OpenFile(filePath, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err = file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maximum {
		return nil, fmt.Errorf("path must remain a bounded regular file")
	}
	if protected && (info.Mode().Perm()&0o400 == 0 || info.Mode().Perm()&0o077 != 0) {
		return nil, fmt.Errorf("protected file permissions are invalid")
	}
	data, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maximum {
		return nil, fmt.Errorf("file exceeds the supported size")
	}
	return data, nil
}

func rejectDuplicatePermissions(values []auth.Permission) error {
	seen := make(map[auth.Permission]struct{}, len(values))
	for _, value := range values {
		if _, exists := seen[value]; exists {
			return fmt.Errorf("permissions contains duplicate value %q", value)
		}
		seen[value] = struct{}{}
	}
	return nil
}

func rejectDuplicateSensitivities(values []string) error {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if _, exists := seen[value]; exists {
			return fmt.Errorf("sensitivities contains duplicate value %q", value)
		}
		seen[value] = struct{}{}
	}
	return nil
}

func rejectDuplicateKeys(node *yaml.Node) error {
	if node == nil {
		return nil
	}
	if node.Kind == yaml.MappingNode {
		seen := make(map[string]struct{}, len(node.Content)/2)
		for index := 0; index+1 < len(node.Content); index += 2 {
			key := node.Content[index]
			if key.Kind != yaml.ScalarNode {
				return fmt.Errorf("mapping keys must be scalar")
			}
			if _, exists := seen[key.Value]; exists {
				return fmt.Errorf("duplicate field %q", key.Value)
			}
			seen[key.Value] = struct{}{}
		}
	}
	for _, child := range node.Content {
		if err := rejectDuplicateKeys(child); err != nil {
			return err
		}
	}
	return nil
}

func hasTopLevelKey(root *yaml.Node, key string) bool {
	if root == nil || len(root.Content) == 0 {
		return false
	}
	document := root.Content[0]
	if document.Kind != yaml.MappingNode {
		return false
	}
	for index := 0; index+1 < len(document.Content); index += 2 {
		if document.Content[index].Value == key {
			return true
		}
	}
	return false
}
