package diff_test

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	lorediff "lore/internal/diff"
	"lore/internal/gitx"
)

func TestGenerateUnifiedDiffHeaders(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git unavailable: %v", err)
	}
	result, err := lorediff.Generate(context.Background(), gitx.New(), []lorediff.Change{
		{Path: "pages/updated.md", Original: []byte("old\n"), Result: []byte("new\n")},
		{Path: "pages/created.md", Result: []byte("created\n"), Created: true},
		{Path: "pages/deleted.md", Original: []byte("deleted\n"), Deleted: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	text := string(result)
	for _, expected := range []string{
		"diff --git a/pages/created.md b/pages/created.md\n",
		"--- /dev/null\n",
		"+++ b/pages/created.md\n",
		"diff --git a/pages/deleted.md b/pages/deleted.md\n",
		"--- a/pages/deleted.md\n",
		"+++ /dev/null\n",
		"-deleted\n",
		"diff --git a/pages/updated.md b/pages/updated.md\n",
		"--- a/pages/updated.md\n",
		"+++ b/pages/updated.md\n",
		"-old\n+new\n",
	} {
		if !strings.Contains(text, expected) {
			t.Errorf("diff missing %q:\n%s", expected, text)
		}
	}
	if strings.Index(text, "pages/created.md") > strings.Index(text, "pages/updated.md") {
		t.Fatalf("diff is not path-sorted:\n%s", text)
	}
}
