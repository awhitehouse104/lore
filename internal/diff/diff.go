package diff

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type Git interface {
	NoIndexDiff(context.Context, string, string, string) ([]byte, error)
}

type Change struct {
	Path     string
	Original []byte
	Result   []byte
	Created  bool
	Deleted  bool
}

func Generate(ctx context.Context, git Git, changes []Change) ([]byte, error) {
	sorted := append([]Change(nil), changes...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Path < sorted[j].Path })
	temp, err := os.MkdirTemp("", "lore-diff-*")
	if err != nil {
		return nil, fmt.Errorf("create diff workspace: %w", err)
	}
	defer os.RemoveAll(temp)

	var combined bytes.Buffer
	for _, change := range sorted {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(change.Path)))
		if clean != change.Path || strings.HasPrefix(clean, "../") || filepath.IsAbs(filepath.FromSlash(clean)) {
			return nil, fmt.Errorf("unsafe diff path %q", change.Path)
		}
		if change.Created && change.Deleted {
			return nil, fmt.Errorf("diff path %q cannot be both created and deleted", change.Path)
		}
		newRelative := "/dev/null"
		if !change.Deleted {
			newRelative = filepath.Join("new", filepath.FromSlash(clean))
			newAbsolute := filepath.Join(temp, newRelative)
			if err := os.MkdirAll(filepath.Dir(newAbsolute), 0o700); err != nil {
				return nil, fmt.Errorf("create diff path: %w", err)
			}
			if err := os.WriteFile(newAbsolute, change.Result, 0o600); err != nil {
				return nil, fmt.Errorf("write diff result: %w", err)
			}
		}
		oldRelative := filepath.Join("old", filepath.FromSlash(clean))
		if change.Created {
			oldRelative = "/dev/null"
		} else {
			oldAbsolute := filepath.Join(temp, oldRelative)
			if err := os.MkdirAll(filepath.Dir(oldAbsolute), 0o700); err != nil {
				return nil, fmt.Errorf("create diff path: %w", err)
			}
			if err := os.WriteFile(oldAbsolute, change.Original, 0o600); err != nil {
				return nil, fmt.Errorf("write diff original: %w", err)
			}
		}
		part, err := git.NoIndexDiff(ctx, temp, filepath.ToSlash(oldRelative), filepath.ToSlash(newRelative))
		if err != nil {
			return nil, fmt.Errorf("generate diff for %s: %w", clean, err)
		}
		part = normalizeHeaders(part, clean, change.Created, change.Deleted)
		if _, err := combined.Write(part); err != nil {
			return nil, err
		}
	}
	return combined.Bytes(), nil
}

func normalizeHeaders(data []byte, path string, created, deleted bool) []byte {
	lines := bytes.SplitAfter(data, []byte{'\n'})
	for index, line := range lines {
		switch {
		case bytes.HasPrefix(line, []byte("diff --git ")):
			lines[index] = []byte("diff --git a/" + path + " b/" + path + lineEnding(line))
		case bytes.HasPrefix(line, []byte("--- ")):
			if created {
				lines[index] = []byte("--- /dev/null" + lineEnding(line))
			} else {
				lines[index] = []byte("--- a/" + path + lineEnding(line))
			}
		case bytes.HasPrefix(line, []byte("+++ ")):
			if deleted {
				lines[index] = []byte("+++ /dev/null" + lineEnding(line))
			} else {
				lines[index] = []byte("+++ b/" + path + lineEnding(line))
			}
		}
	}
	return bytes.Join(lines, nil)
}

func lineEnding(line []byte) string {
	if len(line) > 0 && line[len(line)-1] == '\n' {
		return "\n"
	}
	return ""
}
