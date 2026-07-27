package repository

import (
	"os"
	"testing"
)

func TestAtomicCreateDoesNotOverwrite(t *testing.T) {
	repo := testRepository(t)
	if err := repo.AtomicCreate("sources/2026/07/source.md", []byte("first")); err != nil {
		t.Fatalf("AtomicCreate: %v", err)
	}
	if err := repo.AtomicCreate("sources/2026/07/source.md", []byte("second")); err == nil {
		t.Fatal("second AtomicCreate unexpectedly succeeded")
	}
	data, err := os.ReadFile(repo.Root + "/sources/2026/07/source.md")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "first" {
		t.Fatalf("destination = %q", data)
	}
}
