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
  auto_push_transactions: true
  remote: backup
  require_push: true
capture:
  max_bytes: 1234
index:
  backend: filesystem
  auto_refresh_existing: false
  candidate_multiplier: 10
  minimum_candidates: 100
  maximum_candidates: 1000
`)
	cfg, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.Git.AutoCommitCaptures || !cfg.Git.AutoPushCaptures || !cfg.Git.AutoPushTransactions || cfg.Git.Remote != "backup" || !cfg.Git.RequirePush || cfg.Capture.MaxBytes != 1234 {
		t.Fatalf("unexpected config: %+v", cfg)
	}
	if cfg.Index.Backend != "filesystem" || cfg.Index.AutoRefreshExisting ||
		cfg.Index.CandidateMultiplier != 10 || cfg.Index.MinimumCandidates != 100 || cfg.Index.MaximumCandidates != 1000 {
		t.Fatalf("unexpected index config: %+v", cfg.Index)
	}
}

func TestParseRejectsUnknownKeys(t *testing.T) {
	tests := []string{
		"version: 1\nunknown: true\n",
		"version: 1\ngit:\n  surprise: true\n",
		"version: 1\ncapture:\n  surprise: true\n",
		"version: 1\nindex:\n  surprise: true\n",
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
		"version: 1\ngit:\n  remote: \"--upload-pack=bad\"\n",
		"version: 1\ngit:\n  remote: \"bad remote\"\n",
		"version: 1\nindex:\n  backend: unknown\n",
		"version: 1\nindex:\n  candidate_multiplier: 0\n",
		"version: 1\nindex:\n  minimum_candidates: 100\n  maximum_candidates: 99\n",
		"version: 1\nindex:\n  maximum_candidates: 100001\n",
	}
	for _, data := range tests {
		if _, err := Parse([]byte(data)); err == nil {
			t.Fatalf("Parse(%q) unexpectedly succeeded", data)
		}
	}
}
