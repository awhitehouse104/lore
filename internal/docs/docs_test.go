package docs

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

const validSourceID = "src_01ARZ3NDEKTSV4RRFFQ69G5FAV"

func sourceFile(body string) []byte {
	return []byte(`---
id: ` + validSourceID + `
kind: user_statement
captured_at: 2026-07-22T16:30:21Z
origin: codex
raw_sha256: ` + SHA256([]byte(body)) + `
sensitivity: normal
---
` + body)
}

func TestParsePreservesExactSourceBody(t *testing.T) {
	bodies := []string{
		"without newline",
		"with newline\n",
		"with\r\nCRLF\r\n",
		"Unicode: 🦉 café",
		"",
	}
	for _, body := range bodies {
		t.Run(strings.ReplaceAll(body, "\n", "_"), func(t *testing.T) {
			doc, err := Parse("sources/2026/07/test.md", sourceFile(body))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if string(doc.Body) != body {
				t.Fatalf("body = %q, want %q", doc.Body, body)
			}
			if SHA256(doc.Body) != doc.Source.RawSHA256 {
				t.Fatalf("hash = %q, want %q", SHA256(doc.Body), doc.Source.RawSHA256)
			}
		})
	}
}

func TestValidateSourceAndPage(t *testing.T) {
	sourceDoc, err := Parse("sources/2026/07/test.md", sourceFile("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if errs := Validate(sourceDoc); len(errs) != 0 {
		t.Fatalf("valid source errors: %v", errs)
	}

	pageData := []byte(`---
id: page_project_foo
title: Project Foo
kind: project
aliases: [foo]
created: "2026-07-22"
updated: "2026-07-23"
status: active
sensitivity: normal
tags: [deployment]
---
# Summary
`)
	pageDoc, err := Parse("pages/project-foo.md", pageData)
	if err != nil {
		t.Fatal(err)
	}
	if errs := Validate(pageDoc); len(errs) != 0 {
		t.Fatalf("valid page errors: %v", errs)
	}
}

func TestValidateRejectsInvalidMetadata(t *testing.T) {
	tests := []struct {
		name string
		edit func(*Source)
	}{
		{"id", func(s *Source) { s.ID = "src_bad" }},
		{"kind", func(s *Source) { s.Kind = "Bad Kind" }},
		{"timestamp", func(s *Source) { s.CapturedAt = "yesterday" }},
		{"timestamp_not_utc", func(s *Source) { s.CapturedAt = "2026-07-22T12:00:00-04:00" }},
		{"origin", func(s *Source) { s.Origin = "" }},
		{"hash", func(s *Source) { s.RawSHA256 = "sha256:nope" }},
		{"sensitivity", func(s *Source) { s.Sensitivity = "secret" }},
		{"tag", func(s *Source) { s.Tags = []string{""} }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc, err := Parse("sources/2026/07/test.md", sourceFile("hello"))
			if err != nil {
				t.Fatal(err)
			}
			tt.edit(doc.Source)
			if errs := Validate(doc); len(errs) == 0 {
				t.Fatal("validation unexpectedly succeeded")
			}
		})
	}
}

func TestValidateSourceIDCanonical(t *testing.T) {
	if err := ValidateSourceID(validSourceID); err != nil {
		t.Fatalf("valid source ID: %v", err)
	}
	for _, value := range []string{
		"src_01arz3ndektsv4rrffq69g5fav",
		"src_01ARZ3NDEKTSV4RRFFQ69G5FA",
		"page_01ARZ3NDEKTSV4RRFFQ69G5FAV",
	} {
		if err := ValidateSourceID(value); err == nil {
			t.Fatalf("ValidateSourceID(%q) unexpectedly succeeded", value)
		}
	}
}

func TestValidToken(t *testing.T) {
	for _, value := range []string{"project", "user_statement", "local-note2"} {
		if !ValidToken(value) {
			t.Errorf("ValidToken(%q) = false", value)
		}
	}
	for _, value := range []string{"", "Project", "2project", "has space", "café"} {
		if ValidToken(value) {
			t.Errorf("ValidToken(%q) = true", value)
		}
	}
}

func TestParseRequiresFrontmatter(t *testing.T) {
	for _, data := range [][]byte{
		[]byte("hello"),
		[]byte("---\nid: x\n"),
		[]byte("---\nid: x\n---"),
		[]byte(" ---\nid: x\n---\n"),
	} {
		if _, err := Parse("pages/test.md", data); err == nil {
			t.Fatalf("Parse(%q) unexpectedly succeeded", data)
		}
	}
}

func TestMarkSourceIntegratedPreservesBodyAndUnknownFrontmatter(t *testing.T) {
	body := []byte("first\r\nsecond\nno final newline")
	data := []byte(`---
id: ` + validSourceID + `
kind: user_statement
captured_at: 2026-07-22T16:30:21Z
origin: codex
raw_sha256: ` + SHA256(body) + `
sensitivity: normal
integrated_into: [page_existing]
custom_extension:
  enabled: true
---
`)
	data = append(data, body...)
	updated, err := MarkSourceIntegrated(
		"sources/2026/07/test.md",
		data,
		time.Date(2026, 7, 28, 20, 10, 0, 123, time.UTC),
		[]string{"page_new", "page_existing"},
	)
	if err != nil {
		t.Fatalf("MarkSourceIntegrated: %v", err)
	}
	document, err := Parse("sources/2026/07/test.md", updated)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(document.Body, body) {
		t.Fatalf("body changed:\n%q\n%q", document.Body, body)
	}
	if got := document.Source.IntegratedInto; len(got) != 2 || got[0] != "page_existing" || got[1] != "page_new" {
		t.Fatalf("integrated_into = %v", got)
	}
	if !bytes.Contains(updated[:document.BodyOffset], []byte("custom_extension:")) ||
		!bytes.Contains(updated[:document.BodyOffset], []byte("enabled: true")) {
		t.Fatalf("unknown frontmatter was lost:\n%s", updated[:document.BodyOffset])
	}
	if SHA256(document.Body) != document.Source.RawSHA256 {
		t.Fatal("source body integrity changed")
	}
}

func TestValidateSourceIntegratedInto(t *testing.T) {
	document, err := Parse("sources/2026/07/test.md", sourceFile("hello"))
	if err != nil {
		t.Fatal(err)
	}
	document.Source.IntegratedInto = []string{"page_valid", "page_valid"}
	if errs := Validate(document); len(errs) == 0 {
		t.Fatal("duplicate integrated_into value accepted")
	}
	document.Source.IntegratedInto = []string{"invalid"}
	if errs := Validate(document); len(errs) == 0 {
		t.Fatal("invalid integrated_into value accepted")
	}
}

func TestPageChangedExceptUpdated(t *testing.T) {
	currentData := []byte(`---
id: page_example
title: Example
kind: topic
created: "2026-07-27"
updated: "2026-07-27"
status: active
sensitivity: normal
---
Body.
`)
	updatedOnly := bytes.Replace(currentData, []byte(`updated: "2026-07-27"`), []byte(`updated: "2026-07-28"`), 1)
	changedBody := bytes.Replace(updatedOnly, []byte("Body."), []byte("Changed."), 1)
	current, err := Parse("pages/example.md", currentData)
	if err != nil {
		t.Fatal(err)
	}
	proposed, err := Parse("pages/example.md", updatedOnly)
	if err != nil {
		t.Fatal(err)
	}
	if PageChangedExceptUpdated(current, proposed) {
		t.Fatal("updated-only change reported as another change")
	}
	proposed, err = Parse("pages/example.md", changedBody)
	if err != nil {
		t.Fatal(err)
	}
	if !PageChangedExceptUpdated(current, proposed) {
		t.Fatal("body change was not detected")
	}
	commentChanged := bytes.Replace(updatedOnly, []byte(`updated: "2026-07-28"`), []byte(`updated: "2026-07-28" # corrected`), 1)
	proposed, err = Parse("pages/example.md", commentChanged)
	if err != nil {
		t.Fatal(err)
	}
	if !PageChangedExceptUpdated(current, proposed) {
		t.Fatal("updated-line comment change was not detected")
	}
}
