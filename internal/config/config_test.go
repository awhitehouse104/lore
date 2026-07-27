package config

import "testing"

func TestParseDefaults(t *testing.T) {
	cfg, err := Parse([]byte("version: 1\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := Defaults()
	if cfg != want {
		t.Fatalf("config = %+v, want %+v", cfg, want)
	}
}

func TestParseOverrides(t *testing.T) {
	data := []byte(`version: 1
git:
  auto_commit_captures: false
  auto_push_captures: true
  remote: backup
  require_push: true
capture:
  max_bytes: 1234
`)
	cfg, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.Git.AutoCommitCaptures || !cfg.Git.AutoPushCaptures || cfg.Git.Remote != "backup" || !cfg.Git.RequirePush || cfg.Capture.MaxBytes != 1234 {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}

func TestParseRejectsUnknownKeys(t *testing.T) {
	tests := []string{
		"version: 1\nunknown: true\n",
		"version: 1\ngit:\n  surprise: true\n",
		"version: 1\ncapture:\n  surprise: true\n",
	}
	for _, data := range tests {
		if _, err := Parse([]byte(data)); err == nil {
			t.Fatalf("Parse(%q) unexpectedly succeeded", data)
		}
	}
}

func TestParseValidation(t *testing.T) {
	tests := []string{
		"git: {}\n",
		"version: 2\n",
		"version: 1\ncapture:\n  max_bytes: 0\n",
		"version: 1\ncapture:\n  max_bytes: 67108865\n",
		"version: 1\ngit:\n  remote: \"\"\n",
	}
	for _, data := range tests {
		if _, err := Parse([]byte(data)); err == nil {
			t.Fatalf("Parse(%q) unexpectedly succeeded", data)
		}
	}
}
