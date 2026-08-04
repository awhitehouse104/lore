package markdownlink

import (
	"reflect"
	"testing"
)

func TestDestinations(t *testing.T) {
	line := `[one](../sources/a.md#anchor) [two](<../assets/my image.png>) [three](file(with-parens).md "title")`
	got := Destinations(line)
	want := []string{"../sources/a.md#anchor", "../assets/my image.png", "file(with-parens).md"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("destinations = %v, want %v", got, want)
	}
}

func TestReferenceDefinitionDestination(t *testing.T) {
	got := Destinations(`  [evidence]: ../sources/2026/07/source.md "Source"`)
	want := []string{"../sources/2026/07/source.md"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("destinations = %v, want %v", got, want)
	}
}
