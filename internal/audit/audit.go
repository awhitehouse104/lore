package audit

import (
	"log/slog"
	"regexp"
	"sort"
	"time"
)

var safeFieldPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_./:-]{0,127}$`)

type Event struct {
	RequestID    string
	Transport    string
	Principal    string
	Operation    string
	Outcome      string
	Duration     time.Duration
	ErrorID      string
	WarningCodes []string
}

type Recorder struct {
	logger *slog.Logger
}

func New(logger *slog.Logger) *Recorder {
	return &Recorder{logger: logger}
}

func (r *Recorder) Record(event Event) {
	if r == nil || r.logger == nil {
		return
	}
	requestID := safeField(event.RequestID, "unavailable")
	transport := safeField(event.Transport, "unknown")
	principal := safeField(event.Principal, "unknown")
	operation := safeField(event.Operation, "unknown")
	outcome := safeField(event.Outcome, "error")
	errorID := ""
	if event.ErrorID != "" {
		errorID = safeField(event.ErrorID, "unavailable")
	}
	duration := event.Duration
	if duration < 0 {
		duration = 0
	}
	warnings := make([]string, 0, len(event.WarningCodes))
	seen := make(map[string]struct{}, len(event.WarningCodes))
	for _, code := range event.WarningCodes {
		code = safeField(code, "")
		if code == "" {
			continue
		}
		if _, exists := seen[code]; exists {
			continue
		}
		seen[code] = struct{}{}
		warnings = append(warnings, code)
	}
	sort.Strings(warnings)
	attributes := []any{
		"event", "mcp_operation",
		"request_id", requestID,
		"transport", transport,
		"principal", principal,
		"operation", operation,
		"outcome", outcome,
		"duration_ms", duration.Milliseconds(),
		"warning_codes", warnings,
	}
	if errorID != "" {
		attributes = append(attributes, "error_id", errorID)
	}
	r.logger.Info("Lore MCP audit", attributes...)
}

func (r *Recorder) AuthenticationDenied(transport string) {
	if r == nil || r.logger == nil {
		return
	}
	r.logger.Warn("Lore MCP authentication denied",
		"event", "mcp_authentication",
		"transport", safeField(transport, "unknown"),
		"outcome", "denied",
	)
}

func safeField(value, fallback string) string {
	if !safeFieldPattern.MatchString(value) {
		return fallback
	}
	return value
}
