package id

import (
	"testing"
	"time"

	"lore/internal/docs"
)

func TestCryptoGeneratorFormat(t *testing.T) {
	value, err := (CryptoGenerator{}).New(time.Date(2026, 7, 22, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := docs.ValidateSourceID(value); err != nil {
		t.Fatalf("generated ID %q: %v", value, err)
	}
}
