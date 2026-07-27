package docs

import (
	"strings"
	"testing"
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

func TestParseRequiresFrontmatter(t *testing.T) {
	for _, data := range [][]byte{
		[]byte("hello"),
		[]byte("---\nid: x\n"),
		[]byte(" ---\nid: x\n---\n"),
	} {
		if _, err := Parse("pages/test.md", data); err == nil {
			t.Fatalf("Parse(%q) unexpectedly succeeded", data)
		}
	}
}
