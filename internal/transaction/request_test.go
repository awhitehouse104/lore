package transaction

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

const validRevision = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestDecodeRequestStrictAndValid(t *testing.T) {
	data := []byte(`{
  "schema_version": 1,
  "message": "integrate: test sources",
  "operations": [
    {"op":"create_page","path":"pages/new-page.md","content":"page"},
    {"op":"update_page","path":"pages/old.md","expected_revision":"` + validRevision + `","content":"updated"},
    {"op":"mark_source_integrated","path":"sources/2026/07/source.md","expected_revision":"` + validRevision + `","page_ids":["page_new"]}
  ]
}`)
	request, err := DecodeRequest(data, 1024)
	if err != nil {
		t.Fatalf("DecodeRequest: %v", err)
	}
	if len(request.Operations) != 3 || request.Operations[2].PageIDs[0] != "page_new" {
		t.Fatalf("request = %+v", request)
	}

	invalid := []string{
		`{"schema_version":1,"message":"create: x","operations":[],"extra":true}`,
		`{"schema_version":1,"message":"create: x","operations":[{"op":"create_page","path":"pages/x.md","content":"x","extra":true}]}`,
		`{"schema_version":1,"message":"create: x","operations":[{"op":"create_page","path":"pages/x.md"}]}`,
		`{"schema_version":1,"message":"create: x","operations":[{"op":"unknown","path":"pages/x.md"}]}`,
		`{"schema_version":1,"message":"create: x","operations":[]} trailing`,
	}
	for _, input := range invalid {
		if _, err := DecodeRequest([]byte(input), 1024); err == nil {
			t.Errorf("DecodeRequest accepted %s", input)
		}
	}
}

func TestDecodeRequestBoundsAndDuplicatePaths(t *testing.T) {
	operations := make([]map[string]any, MaxOperations+1)
	for index := range operations {
		operations[index] = map[string]any{
			"op":      "create_page",
			"path":    fmt.Sprintf("pages/p-%d.md", index),
			"content": "x",
		}
	}
	data, err := json.Marshal(map[string]any{
		"schema_version": 1,
		"message":        "create: many",
		"operations":     operations,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeRequest(data, 1024); err == nil {
		t.Fatal("operation limit was not enforced")
	}

	duplicate := []byte(`{"schema_version":1,"message":"update: duplicate","operations":[
{"op":"create_page","path":"pages/x.md","content":"x"},
{"op":"update_page","path":"pages/x.md","expected_revision":"` + validRevision + `","content":"y"}]}`)
	if _, err := DecodeRequest(duplicate, 1024); err == nil {
		t.Fatal("duplicate path was accepted")
	}

	tooLarge := []byte(`{"schema_version":1,"message":"create: large","operations":[
{"op":"create_page","path":"pages/x.md","content":"` + strings.Repeat("x", 11) + `"}]}`)
	if _, err := DecodeRequest(tooLarge, 10); err == nil {
		t.Fatal("page size limit was not enforced")
	}
	if _, err := DecodeRequest(make([]byte, MaxRequestBytes+1), 1024); err == nil {
		t.Fatal("request size limit was not enforced")
	}
}

func TestValidateMessage(t *testing.T) {
	valid := []string{
		"integrate: x", "create: x", "update: x", "correct: x",
		"archive: x", "maintenance: x",
	}
	for _, message := range valid {
		if err := ValidateMessage(message); err != nil {
			t.Errorf("ValidateMessage(%q): %v", message, err)
		}
	}
	invalid := []string{"", "capture: x", "create:\nx", "create:\tx", strings.Repeat("a", 161)}
	for _, message := range invalid {
		if err := ValidateMessage(message); err == nil {
			t.Errorf("ValidateMessage accepted %q", message)
		}
	}
}

func TestValidateOperationPaths(t *testing.T) {
	for _, path := range []string{"pages/nested/x.md", "pages/X.md", "pages/a_b.md", "../pages/x.md", `pages\x.md`} {
		if err := ValidatePagePath(path); err == nil {
			t.Errorf("ValidatePagePath accepted %q", path)
		}
	}
	for _, path := range []string{"sources/../pages/x.md", "/sources/x.md", `sources\x.md`, "sources/x.txt"} {
		if err := ValidateSourcePath(path); err == nil {
			t.Errorf("ValidateSourcePath accepted %q", path)
		}
	}
}

func TestValidateGitHashRejectsRevisionOptionInjection(t *testing.T) {
	for _, value := range []string{
		"0123456789012345678901234567890123456789",
		strings.Repeat("a", 64),
	} {
		if err := ValidateGitHash(value); err != nil {
			t.Errorf("ValidateGitHash(%q): %v", value, err)
		}
	}
	for _, value := range []string{"", "HEAD", "--all", strings.Repeat("A", 40), strings.Repeat("a", 39)} {
		if err := ValidateGitHash(value); err == nil {
			t.Errorf("ValidateGitHash accepted %q", value)
		}
	}
}
