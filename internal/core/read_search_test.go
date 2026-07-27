package core

import (
	"bytes"
	"testing"
)

func TestParseLineRange(t *testing.T) {
	got, err := ParseLineRange("10:20")
	if err != nil || got != (LineRange{Start: 10, End: 20}) {
		t.Fatalf("ParseLineRange = %+v, %v", got, err)
	}
	for _, value := range []string{"", "1", "1:", ":2", "0:1", "-1:2", "2:1", "a:b", "1:2:3"} {
		if _, err := ParseLineRange(value); err == nil {
			t.Fatalf("ParseLineRange(%q) unexpectedly succeeded", value)
		}
	}
}

func TestSliceLinesAndClamp(t *testing.T) {
	data := []byte("one\ntwo\nthree\n")
	got, start, end, err := sliceLines(data, &LineRange{Start: 2, End: 20})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, []byte("two\nthree\n")) || start != 2 || end != 3 {
		t.Fatalf("slice = %q %d:%d", got, start, end)
	}
	all, start, end, err := sliceLines([]byte("no newline"), nil)
	if err != nil || string(all) != "no newline" || start != 1 || end != 1 {
		t.Fatalf("all = %q %d:%d err=%v", all, start, end, err)
	}
	if _, _, _, err := sliceLines(data, &LineRange{Start: 4, End: 5}); err == nil {
		t.Fatal("out-of-bounds start unexpectedly succeeded")
	}
}
