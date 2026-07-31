package serverconfig

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"lore/internal/auth"
	"lore/internal/gitx"
	"lore/internal/initrepo"
)

func TestParseValidConfigurationAndDefaults(t *testing.T) {
	repo, tokenFile, token := configFixture(t)
	config, err := Parse([]byte(validConfig(repo, tokenFile, "")))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if config.Version != 1 || config.Repo != repo || config.Listen != DefaultListen ||
		config.Endpoint != DefaultEndpoint || !config.IsLoopback() {
		t.Fatalf("config defaults = %+v", config)
	}
	if config.Transport.RequestMaxBytes != DefaultRequestMaxBytes ||
		config.Transport.ResponseMaxBytes != DefaultResponseMaxBytes ||
		config.Transport.MaxConcurrentRequests != DefaultMaxConcurrentRequests ||
		config.Transport.RateLimit.RequestsPerMinute != DefaultRateLimitRequestsPerMinute ||
		config.Transport.RateLimit.BurstRequests != DefaultRateLimitBurstRequests ||
		config.Transport.RequestTimeout.Value() != DefaultRequestTimeout ||
		config.Transport.ShutdownTimeout.Value() != DefaultShutdownTimeout {
		t.Fatalf("transport defaults = %+v", config.Transport)
	}
	if len(config.BearerPrincipals) != 1 || config.BearerPrincipals[0].Principal.ID != "remote_reader" {
		t.Fatalf("bearer principals = %+v", config.BearerPrincipals)
	}
	authenticator := auth.NewBearerAuthenticator(config.BearerPrincipals)
	principal, ok := authenticator.Authenticate([]string{"Bearer " + token})
	if !ok || principal.ID != "remote_reader" {
		t.Fatalf("loaded token did not authenticate: %+v, %v", principal, ok)
	}
}

func TestParseRejectsUnknownDuplicateAndInvalidPrincipalFields(t *testing.T) {
	repo, tokenFile, _ := configFixture(t)
	secondToken := writeToken(t, filepath.Join(t.TempDir(), "second.token"), "abcdef0123456789abcdef0123456789")
	tests := []string{
		validConfig(repo, tokenFile, "surprise: true\n"),
		strings.Replace(validConfig(repo, tokenFile, ""), "version: 1\n", "version: 1\nversion: 1\n", 1),
		validConfig(repo, tokenFile, fmt.Sprintf(`auth:
  tokens:
    - name: remote_reader
      token_file: %s
      permissions: [query]
      sensitivities: [normal]
    - name: remote_reader
      token_file: %s
      permissions: [query]
      sensitivities: [normal]
`, secondToken, tokenFile)),
		strings.Replace(validConfig(repo, tokenFile, ""), "permissions: [query]", "permissions: [query, query]", 1),
		strings.Replace(validConfig(repo, tokenFile, ""), "permissions: [query]", "permissions: [admin]", 1),
		strings.Replace(validConfig(repo, tokenFile, ""), "sensitivities: [normal]", "sensitivities: [normal, normal]", 1),
		strings.Replace(validConfig(repo, tokenFile, ""), "sensitivities: [normal]", "sensitivities: [local-only]", 1),
	}
	for index, data := range tests {
		if _, err := Parse([]byte(data)); err == nil {
			t.Errorf("case %d unexpectedly succeeded", index)
		}
	}
}

func TestParseRejectsDuplicateTokensWithoutLeakingThem(t *testing.T) {
	repo, tokenFile, token := configFixture(t)
	second := filepath.Join(t.TempDir(), "duplicate.token")
	if err := os.WriteFile(second, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	data := fmt.Sprintf(`version: 1
repo: %s
auth:
  tokens:
    - name: first_reader
      token_file: %s
      permissions: [query]
      sensitivities: [normal]
    - name: second_reader
      token_file: %s
      permissions: [query]
      sensitivities: [normal]
`, repo, tokenFile, second)
	_, err := Parse([]byte(data))
	if err == nil || strings.Contains(err.Error(), token) {
		t.Fatalf("duplicate error = %v", err)
	}
}

func TestTokenFileProtectionAndContent(t *testing.T) {
	repo, tokenFile, token := configFixture(t)
	outside := filepath.Join(t.TempDir(), "outside.token")
	if err := os.WriteFile(outside, []byte(token), 0o600); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(t.TempDir(), "link.token")
	if err := os.Symlink(outside, symlink); err != nil {
		t.Fatal(err)
	}
	loose := filepath.Join(t.TempDir(), "loose.token")
	if err := os.WriteFile(loose, []byte(token), 0o644); err != nil {
		t.Fatal(err)
	}
	short := writeToken(t, filepath.Join(t.TempDir(), "short.token"), "too-short")
	multiline := filepath.Join(t.TempDir(), "multiline.token")
	if err := os.WriteFile(multiline, []byte(token+"\n"+token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{symlink, loose, short, multiline} {
		_, err := Parse([]byte(validConfig(repo, file, "")))
		if err == nil {
			t.Errorf("token file %s unexpectedly succeeded", filepath.Base(file))
		}
		if err != nil && strings.Contains(err.Error(), token) {
			t.Errorf("token leaked in error: %v", err)
		}
	}
	_ = tokenFile
}

func TestTransportEndpointOriginAndListenValidation(t *testing.T) {
	repo, tokenFile, _ := configFixture(t)
	valid := validConfig(repo, tokenFile, "")
	replacements := [][2]string{
		{"endpoint: /mcp\n", "endpoint: /a/../mcp\n"},
		{"endpoint: /mcp\n", "endpoint: /mcp?debug=true\n"},
		{"endpoint: /mcp\n", "endpoint: /health/live\n"},
		{"request_max_bytes: 8388608", "request_max_bytes: 100"},
		{"response_max_bytes: 8388608", "response_max_bytes: 100"},
		{"max_concurrent_requests: 8", "max_concurrent_requests: 0"},
		{"requests_per_minute: 600", "requests_per_minute: 0"},
		{"burst_requests: 128", "burst_requests: 0"},
		{"burst_requests: 128", "burst_requests: 7"},
		{"request_timeout: 60s", "request_timeout: 500ms"},
		{"shutdown_timeout: 15s", "shutdown_timeout: 3m"},
		{"trust_forwarded_headers: false", "trust_forwarded_headers: true"},
		{"listen: 127.0.0.1:8787\n", "listen: localhost:8787\n"},
		{"listen: 127.0.0.1:8787\n", "listen: 0.0.0.0:8787\n"},
	}
	for index, replacement := range replacements {
		data := strings.Replace(valid, replacement[0], replacement[1], 1)
		if _, err := Parse([]byte(data)); err == nil {
			t.Errorf("case %d unexpectedly succeeded", index)
		}
	}

	nonLoopback := strings.Replace(valid, "listen: 127.0.0.1:8787", "listen: 192.0.2.1:8787", 1)
	if _, err := Parse([]byte(nonLoopback)); err == nil {
		t.Fatal("plaintext non-loopback listen unexpectedly succeeded")
	}
	nonLoopback = strings.Replace(nonLoopback, "allow_plaintext_non_loopback: false", "allow_plaintext_non_loopback: true", 1)
	config, err := Parse([]byte(nonLoopback))
	if err != nil || config.IsLoopback() {
		t.Fatalf("explicit non-loopback override = %+v, %v", config, err)
	}

	origins := strings.Replace(valid, "allowed_origins: []", "allowed_origins: [https://EXAMPLE.com, http://127.0.0.1:8080]", 1)
	config, err = Parse([]byte(origins))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"https://example.com:443", "http://127.0.0.1:8080"}
	if fmt.Sprint(config.NormalizedOrigins) != fmt.Sprint(want) {
		t.Fatalf("normalized origins = %v, want %v", config.NormalizedOrigins, want)
	}
	duplicateOrigins := strings.Replace(valid, "allowed_origins: []", "allowed_origins: [https://example.com, https://example.com:443]", 1)
	if _, err := Parse([]byte(duplicateOrigins)); err == nil {
		t.Fatal("equivalent duplicate origins unexpectedly succeeded")
	}
}

func TestTransportRateLimitDefaultsAndBounds(t *testing.T) {
	repo, tokenFile, _ := configFixture(t)
	base := validConfig(repo, tokenFile, "")
	block := `  rate_limit:
    requests_per_minute: 600
    burst_requests: 128
`
	withoutBlock := strings.Replace(base, block, "", 1)
	config, err := Parse([]byte(withoutBlock))
	if err != nil {
		t.Fatal(err)
	}
	if config.Transport.RateLimit.RequestsPerMinute != DefaultRateLimitRequestsPerMinute ||
		config.Transport.RateLimit.BurstRequests != DefaultRateLimitBurstRequests {
		t.Fatalf("omitted rate limit defaults = %+v", config.Transport.RateLimit)
	}
	partial := strings.Replace(base, "    requests_per_minute: 600\n", "", 1)
	config, err = Parse([]byte(partial))
	if err != nil {
		t.Fatal(err)
	}
	if config.Transport.RateLimit.RequestsPerMinute != DefaultRateLimitRequestsPerMinute ||
		config.Transport.RateLimit.BurstRequests != DefaultRateLimitBurstRequests {
		t.Fatalf("partial rate limit defaults = %+v", config.Transport.RateLimit)
	}

	maximum := strings.Replace(
		base,
		"requests_per_minute: 600",
		fmt.Sprintf("requests_per_minute: %d", MaximumRateLimitRequestsPerMinute),
		1,
	)
	maximum = strings.Replace(
		maximum,
		"burst_requests: 128",
		fmt.Sprintf("burst_requests: %d", MaximumRateLimitBurstRequests),
		1,
	)
	if _, err := Parse([]byte(maximum)); err != nil {
		t.Fatalf("maximum rate limit: %v", err)
	}

	for _, replacement := range [][2]string{
		{
			"requests_per_minute: 600",
			fmt.Sprintf("requests_per_minute: %d", MaximumRateLimitRequestsPerMinute+1),
		},
		{
			"burst_requests: 128",
			fmt.Sprintf("burst_requests: %d", MaximumRateLimitBurstRequests+1),
		},
	} {
		data := strings.Replace(base, replacement[0], replacement[1], 1)
		if _, err := Parse([]byte(data)); err == nil {
			t.Fatalf("rate limit replacement %q unexpectedly succeeded", replacement[1])
		}
	}
}

func TestTransportAggregatePayloadLimits(t *testing.T) {
	repo, tokenFile, _ := configFixture(t)
	base := validConfig(repo, tokenFile, "")
	tests := []struct {
		name        string
		requestMax  string
		responseMax string
		concurrent  string
		wantError   string
	}{
		{
			name:        "defaults at aggregate boundaries",
			requestMax:  "8388608",
			responseMax: "8388608",
			concurrent:  "8",
		},
		{
			name:        "single maximum-size request",
			requestMax:  "67108864",
			responseMax: "67108864",
			concurrent:  "1",
		},
		{
			name:        "maximum concurrency with one MiB payloads",
			requestMax:  "1048576",
			responseMax: "1048576",
			concurrent:  "64",
		},
		{
			name:        "aggregate request bytes exceed boundary",
			requestMax:  "8388609",
			responseMax: "8388608",
			concurrent:  "8",
			wantError:   "transport.request_max_bytes multiplied by transport.max_concurrent_requests",
		},
		{
			name:        "aggregate response bytes exceed boundary",
			requestMax:  "8388608",
			responseMax: "8388609",
			concurrent:  "8",
			wantError:   "transport.response_max_bytes multiplied by transport.max_concurrent_requests",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			data := strings.Replace(base, "request_max_bytes: 8388608", "request_max_bytes: "+test.requestMax, 1)
			data = strings.Replace(data, "response_max_bytes: 8388608", "response_max_bytes: "+test.responseMax, 1)
			data = strings.Replace(data, "max_concurrent_requests: 8", "max_concurrent_requests: "+test.concurrent, 1)
			_, err := Parse([]byte(data))
			if test.wantError == "" {
				if err != nil {
					t.Fatalf("Parse: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("Parse error = %v, want containing %q", err, test.wantError)
			}
		})
	}
}

func TestAggregateLimitCheckDoesNotMultiply(t *testing.T) {
	if !exceedsAggregateLimit(int64(^uint64(0)>>1), 2, MaximumAggregateResponseBytes) {
		t.Fatal("maximum int64 per-request value unexpectedly fit the aggregate limit")
	}
	if exceedsAggregateLimit(MaximumAggregateResponseBytes/2, 2, MaximumAggregateResponseBytes) {
		t.Fatal("exact aggregate boundary unexpectedly exceeded the limit")
	}
}

func TestLoadRejectsSymlinkConfiguration(t *testing.T) {
	repo, tokenFile, _ := configFixture(t)
	target := filepath.Join(t.TempDir(), "mcp.yaml")
	if err := os.WriteFile(target, []byte(validConfig(repo, tokenFile, "")), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "mcp-link.yaml")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(link); err == nil {
		t.Fatal("symlink configuration unexpectedly succeeded")
	}
}

func validConfig(repo, tokenFile, extra string) string {
	base := fmt.Sprintf(`version: 1
repo: %s
listen: 127.0.0.1:8787
endpoint: /mcp
transport:
  request_max_bytes: 8388608
  response_max_bytes: 8388608
  max_concurrent_requests: 8
  rate_limit:
    requests_per_minute: 600
    burst_requests: 128
  request_timeout: 60s
  shutdown_timeout: 15s
  allowed_origins: []
  trust_forwarded_headers: false
  allow_plaintext_non_loopback: false
auth:
  tokens:
    - name: remote_reader
      token_file: %s
      permissions: [query]
      sensitivities: [normal]
logging:
  format: json
  level: info
  destination: stderr
`, repo, tokenFile)
	if strings.HasPrefix(extra, "auth:") {
		start := strings.Index(base, "auth:\n")
		end := strings.Index(base, "logging:\n")
		return base[:start] + extra + base[end:]
	}
	return base + extra
}

func configFixture(t *testing.T) (string, string, string) {
	t.Helper()
	repo := filepath.Join(t.TempDir(), "knowledge")
	if _, err := initrepo.Initialize(context.Background(), initrepo.Options{Path: repo, NoGit: true}, gitx.New()); err != nil {
		t.Fatal(err)
	}
	token := base64.RawURLEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	tokenFile := filepath.Join(t.TempDir(), "remote.token")
	if err := os.WriteFile(tokenFile, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return repo, tokenFile, token
}

func writeToken(t *testing.T, filePath, raw string) string {
	t.Helper()
	token := base64.RawURLEncoding.EncodeToString([]byte(raw))
	if err := os.WriteFile(filePath, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return filePath
}
