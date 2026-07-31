package audit

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestRecorderEmitsOnlySanitizedMetadata(t *testing.T) {
	var output bytes.Buffer
	recorder := New(slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug})))
	secret := "seeded capture body and bearer token"
	recorder.Record(Event{
		RequestID:    "req_01ARZ3NDEKTSV4RRFFQ69G5FAV",
		Transport:    "http",
		Principal:    "remote_reader",
		Operation:    secret,
		Outcome:      "success",
		Duration:     42 * time.Millisecond,
		ErrorID:      secret,
		WarningCodes: []string{"index_stale", secret, "index_stale"},
	})
	denialSecret := "seeded_secret_token"
	recorder.AuthenticationDenied("http", AuthenticationDenialMissingCredentials)
	recorder.AuthenticationDenied("http", AuthenticationDenialReason(denialSecret))
	logged := output.String()
	if strings.Contains(logged, secret) || strings.Contains(logged, denialSecret) {
		t.Fatalf("audit log leaked seeded secret: %s", logged)
	}
	for _, expected := range []string{
		`"event":"mcp_operation"`,
		`"request_id":"req_01ARZ3NDEKTSV4RRFFQ69G5FAV"`,
		`"principal":"remote_reader"`,
		`"operation":"unknown"`,
		`"duration_ms":42`,
		`"event":"mcp_authentication"`,
		`"reason":"missing_credentials"`,
		`"reason":"invalid_credentials"`,
	} {
		if !strings.Contains(logged, expected) {
			t.Errorf("audit log missing %s: %s", expected, logged)
		}
	}
}
