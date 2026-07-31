package mcpserver

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"lore/internal/auth"
	"lore/internal/catalog"
	"lore/internal/core"
	"lore/internal/gitx"
	"lore/internal/initrepo"
	"lore/internal/repository"
)

func BenchmarkHTTPServerConstruction(b *testing.B) {
	for _, documents := range []int{1_000, 10_000} {
		b.Run(fmt.Sprintf("documents_%d", documents), func(b *testing.B) {
			service := benchmarkResourceService(b, documents)
			principal := benchmarkResourcePrincipal(b, auth.TransportHTTP)
			scanner := resourceCatalogScanner(func(context.Context, *repository.Repository) (catalog.Catalog, error) {
				b.Fatal("HTTP server construction invoked the resource scanner")
				return catalog.Catalog{}, nil
			})
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				_ = newWithResourceScanner(context.Background(), service, principal, slog.New(slog.DiscardHandler), scanner)
			}
		})
	}
}

func BenchmarkResourceEnumeration(b *testing.B) {
	// Cold and warm refer to Lore's per-server resource registry. These
	// benchmarks intentionally do not claim control over the kernel page cache.
	for _, documents := range []int{1_000, 10_000} {
		service := benchmarkResourceService(b, documents)
		b.Run(fmt.Sprintf("cold_HTTP_registry/documents_%d", documents), func(b *testing.B) {
			principal := benchmarkResourcePrincipal(b, auth.TransportHTTP)
			logger := slog.New(slog.DiscardHandler)
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				server := NewWithContext(context.Background(), service, principal, logger)
				if err := server.ensurePageResources(context.Background()); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(fmt.Sprintf("warm_stdio_registry/documents_%d", documents), func(b *testing.B) {
			principal := benchmarkResourcePrincipal(b, auth.TransportStdio)
			server := NewWithContext(context.Background(), service, principal, slog.New(slog.DiscardHandler))
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if err := server.ensurePageResources(context.Background()); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func benchmarkResourceService(b *testing.B, documents int) *core.Service {
	b.Helper()
	root := filepath.Join(b.TempDir(), "knowledge")
	if _, err := initrepo.Initialize(context.Background(), initrepo.Options{Path: root, NoGit: true}, gitx.New()); err != nil {
		b.Fatal(err)
	}
	for index := range documents {
		data := fmt.Appendf(nil, `---
id: page_benchmark_%05d
title: Benchmark Page %05d
kind: topic
created: "2026-07-30"
updated: "2026-07-30"
status: active
sensitivity: normal
---
Representative benchmark content.
`, index, index)
		path := filepath.Join(root, "pages", fmt.Sprintf("benchmark-%05d.md", index))
		if err := os.WriteFile(path, data, 0o644); err != nil {
			b.Fatal(err)
		}
	}
	repo, err := repository.Open(root)
	if err != nil {
		b.Fatal(err)
	}
	return core.NewService(repo)
}

func benchmarkResourcePrincipal(b *testing.B, transport auth.Transport) auth.Principal {
	b.Helper()
	principal, err := auth.NewPrincipal("benchmark_reader", transport, []auth.Permission{auth.PermissionQuery}, []string{"normal"})
	if err != nil {
		b.Fatal(err)
	}
	return principal
}
