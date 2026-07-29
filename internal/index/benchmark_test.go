package index

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"lore/internal/gitx"
)

func BenchmarkBuildGeneratedCorpus(b *testing.B) {
	b.StopTimer()
	repo := newTestRepository(b)
	const documentCount = 10_000
	body := strings.Repeat("Medium benchmark knowledge keeps deterministic retrieval measurable. ", 12)
	var canonicalBytes int64
	for number := 0; number < documentCount; number++ {
		data := []byte(fmt.Sprintf(`---
id: page_benchmark_%05d
title: Benchmark Note %05d
kind: note
created: "2026-07-29"
updated: "2026-07-29"
status: active
sensitivity: normal
tags:
  - benchmark
---
%s
`, number, number, body))
		path := filepath.Join(repo.Root, "pages", fmt.Sprintf("benchmark-%05d.md", number))
		if err := os.WriteFile(path, data, 0o644); err != nil {
			b.Fatal(err)
		}
		canonicalBytes += int64(len(data))
	}
	manager := NewManager(repo, gitx.New(), "0.3.0-benchmark")
	b.ReportAllocs()
	b.ResetTimer()
	b.StartTimer()
	for iteration := 0; iteration < b.N; iteration++ {
		if _, err := manager.Build(context.Background(), BuildOptions{Force: true}); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	info, err := os.Stat(filepath.Join(repo.Root, filepath.FromSlash(RelativeIndexPath)))
	if err != nil {
		b.Fatal(err)
	}
	b.ReportMetric(float64(documentCount), "documents/op")
	b.ReportMetric(float64(info.Size())/float64(canonicalBytes), "index/text")
}
