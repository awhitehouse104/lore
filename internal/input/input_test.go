package input

import (
	"strings"
	"testing"
)

func TestReadBounded(t *testing.T) {
	data, err := ReadBounded(strings.NewReader("café"), 5)
	if err != nil {
		t.Fatalf("ReadBounded: %v", err)
	}
	if string(data) != "café" {
		t.Fatalf("data = %q", data)
	}
	if _, err := ReadBounded(strings.NewReader("abcdef"), 5); err == nil {
		t.Fatal("oversized input unexpectedly succeeded")
	}
	if _, err := ReadBounded(strings.NewReader(string([]byte{0xff})), 5); err == nil {
		t.Fatal("invalid UTF-8 unexpectedly succeeded")
	}
}
