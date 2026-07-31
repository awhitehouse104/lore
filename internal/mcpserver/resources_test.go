package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"lore/internal/auth"
	"lore/internal/catalog"
	"lore/internal/repository"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestAuthorizedResourceListingTemplatesAndReads(t *testing.T) {
	service := newTestService(t)
	principal := testPrincipal(t, "normal_reader", []auth.Permission{auth.PermissionQuery}, []string{"normal"})
	client := connectTestClient(t, service, principal)

	list, err := client.ListResources(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if list.CacheScope != "private" || list.TTLMs != 0 || list.NextCursor != "" || len(list.Resources) != 1 {
		t.Fatalf("resource list = %+v", list)
	}
	resource := list.Resources[0]
	if resource.URI != "lore://pages/page_project_foo" ||
		resource.Name != "page_project_foo" ||
		resource.Title != "Project Foo" ||
		resource.MIMEType != resourceMIMEType ||
		strings.Contains(resource.Description, "Sensitive") {
		t.Fatalf("listed resource = %+v", resource)
	}

	templates, err := client.ListResourceTemplates(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if templates.CacheScope != "private" || templates.TTLMs != 0 || len(templates.ResourceTemplates) != 2 {
		t.Fatalf("resource templates = %+v", templates)
	}
	if templates.ResourceTemplates[0].URITemplate != "lore://pages/{id}" ||
		templates.ResourceTemplates[1].URITemplate != "lore://sources/{id}" {
		t.Fatalf("resource template order = %+v", templates.ResourceTemplates)
	}

	read, err := client.ReadResource(t.Context(), &mcp.ReadResourceParams{URI: resource.URI})
	if err != nil {
		t.Fatal(err)
	}
	if read.CacheScope != "private" || read.TTLMs != 0 || len(read.Contents) != 1 ||
		read.Contents[0].MIMEType != resourceMIMEType ||
		!strings.Contains(read.Contents[0].Text, "Project Foo must remain deployable") ||
		read.Contents[0].Meta["lore/revision"] == "" {
		t.Fatalf("page resource read = %+v", read)
	}

	sourceURI := "lore://sources/src_01ARZ3NDEKTSV4RRFFQ69G5FAV"
	source, err := client.ReadResource(t.Context(), &mcp.ReadResourceParams{URI: sourceURI})
	if err != nil || len(source.Contents) != 1 ||
		!strings.Contains(source.Contents[0].Text, "Normal evidence for transaction authorization.") {
		t.Fatalf("exact source resource = %+v, %v", source, err)
	}
}

func TestHTTPResourceEnumerationIsLazy(t *testing.T) {
	service := newTestService(t)
	principal := httpQueryPrincipal(t, "remote_reader", []string{"normal"})
	var scans atomic.Int32
	scanner := func(ctx context.Context, repo *repository.Repository) (catalog.Catalog, error) {
		scans.Add(1)
		return scanResourceCatalog(ctx, repo)
	}
	server := newWithResourceScanner(t.Context(), service, principal, nil, scanner)
	if scans.Load() != 0 {
		t.Fatal("HTTP server construction scanned the resource catalog")
	}
	client := connectClientToServer(t, server)
	if scans.Load() != 0 {
		t.Fatal("HTTP discovery scanned the resource catalog")
	}
	capabilities := client.InitializeResult().Capabilities
	if capabilities.Resources == nil || capabilities.Resources.ListChanged {
		t.Fatalf("HTTP resource capabilities = %+v", capabilities.Resources)
	}
	if _, err := client.ListTools(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ListResourceTemplates(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ReadResource(t.Context(), &mcp.ReadResourceParams{URI: "lore://pages/page_project_foo"}); err != nil {
		t.Fatal(err)
	}
	if scans.Load() != 0 {
		t.Fatalf("unrelated HTTP operations performed %d resource scan(s)", scans.Load())
	}

	first, err := client.ListResources(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Resources) != 1 || scans.Load() != 1 {
		t.Fatalf("first resource list = %d resources, %d scans", len(first.Resources), scans.Load())
	}
	second, err := client.ListResources(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Resources) != 1 || scans.Load() != 1 {
		t.Fatalf("second resource list = %d resources, %d scans", len(second.Resources), scans.Load())
	}
}

func TestHTTPCommitSkipsUnloadedResourceRefresh(t *testing.T) {
	service := newTestService(t)
	principal, err := auth.NewPrincipal(
		"remote_curator",
		auth.TransportHTTP,
		[]auth.Permission{auth.PermissionQuery, auth.PermissionCurate},
		[]string{"normal"},
	)
	if err != nil {
		t.Fatal(err)
	}
	var scans atomic.Int32
	scanner := func(ctx context.Context, repo *repository.Repository) (catalog.Catalog, error) {
		scans.Add(1)
		return scanResourceCatalog(ctx, repo)
	}
	client := connectClientToServer(t, newWithResourceScanner(t.Context(), service, principal, nil, scanner))
	preview := decodeOutput[PreviewOutput](t, callTool(t, client, "lore_preview", map[string]any{
		"schema_version": 1,
		"message":        "create: lazy resource",
		"operations": []any{map[string]any{
			"op":   "create_page",
			"path": "pages/lazy-resource.md",
			"content": `---
id: page_lazy_resource
title: Lazy Resource
kind: topic
created: "2026-07-29"
updated: "2026-07-29"
status: active
sensitivity: normal
---
Lazy resource registration.
`,
		}},
	}))
	_ = callTool(t, client, "lore_commit", map[string]any{
		"transaction_id": preview.TransactionID,
		"preview_digest": preview.PreviewDigest,
	})
	if scans.Load() != 0 {
		t.Fatalf("HTTP commit performed %d unloaded resource scan(s)", scans.Load())
	}
	list, err := client.ListResources(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if scans.Load() != 1 {
		t.Fatalf("post-commit resource list performed %d scans", scans.Load())
	}
	found := false
	for _, resource := range list.Resources {
		if resource.URI == "lore://pages/page_lazy_resource" {
			found = true
		}
	}
	if !found {
		t.Fatalf("committed page absent from lazy resource list: %+v", list.Resources)
	}
}

func TestConcurrentHTTPResourceListsShareOneServerScan(t *testing.T) {
	service := newTestService(t)
	principal := httpQueryPrincipal(t, "remote_reader", []string{"normal"})
	var scans atomic.Int32
	entered := make(chan struct{})
	release := make(chan struct{})
	var enteredOnce sync.Once
	scanner := func(ctx context.Context, repo *repository.Repository) (catalog.Catalog, error) {
		scans.Add(1)
		enteredOnce.Do(func() { close(entered) })
		select {
		case <-ctx.Done():
			return catalog.Catalog{}, ctx.Err()
		case <-release:
			return scanResourceCatalog(ctx, repo)
		}
	}
	client := connectClientToServer(t, newWithResourceScanner(t.Context(), service, principal, nil, scanner))
	results := make(chan error, 2)
	go func() {
		_, err := client.ListResources(t.Context(), nil)
		results <- err
	}()
	<-entered
	go func() {
		_, err := client.ListResources(t.Context(), nil)
		results <- err
	}()
	close(release)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if scans.Load() != 1 {
		t.Fatalf("concurrent resource lists performed %d scans", scans.Load())
	}
}

func TestLazyResourceScanFailureIsGenericAndRetryable(t *testing.T) {
	service := newTestService(t)
	principal := httpQueryPrincipal(t, "remote_reader", []string{"normal"})
	var scans atomic.Int32
	const secret = "catalog-path-secret"
	scanner := func(context.Context, *repository.Repository) (catalog.Catalog, error) {
		scans.Add(1)
		return catalog.Catalog{}, errors.New(secret)
	}
	client := connectClientToServer(t, newWithResourceScanner(t.Context(), service, principal, nil, scanner))
	if _, err := client.ListTools(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	for attempt := int32(1); attempt <= 2; attempt++ {
		_, err := client.ListResources(t.Context(), nil)
		if err == nil || !strings.Contains(err.Error(), errResourceCatalogUnavailable.Error()) ||
			strings.Contains(err.Error(), secret) {
			t.Fatalf("attempt %d error = %v", attempt, err)
		}
		if scans.Load() != attempt {
			t.Fatalf("attempt %d scan count = %d", attempt, scans.Load())
		}
	}
}

func TestResourcesMaskUnauthorizedAndRejectURIConfusion(t *testing.T) {
	service := newTestService(t)
	principal := testPrincipal(t, "normal_reader", []auth.Permission{auth.PermissionQuery}, []string{"normal"})
	client := connectTestClient(t, service, principal)
	tests := []string{
		"lore://pages/page_sensitive_notes",
		"lore://pages/page_local_notes",
		"lore://pages/../page_project_foo",
		"lore://pages/page_project_foo/extra",
		"lore://pages/%2e%2e%2fpage_project_foo",
		"lore://sources/page_project_foo",
		"lore://pages/src_01ARZ3NDEKTSV4RRFFQ69G5FAV",
		"lore://pages/page_project_foo?query=secret",
		"lore://pages/page_project_foo#fragment",
		"file:///etc/passwd",
	}
	for _, uri := range tests {
		_, err := client.ReadResource(t.Context(), &mcp.ReadResourceParams{URI: uri})
		if err == nil {
			t.Errorf("ReadResource(%q) unexpectedly succeeded", uri)
			continue
		}
		message := err.Error()
		for _, secret := range []string{"Sensitive Notes", "Local Notes", "sensitive-notes.md", "local-notes.md"} {
			if strings.Contains(message, secret) {
				t.Errorf("ReadResource(%q) leaked %q: %v", uri, secret, err)
			}
		}
	}
}

func TestResourcePaginationIsDeterministicAndPrivate(t *testing.T) {
	service := newTestService(t)
	for index := 0; index < 101; index++ {
		id := fmt.Sprintf("page_bulk_%03d", index)
		data := fmt.Appendf(nil, `---
id: %s
title: Bulk %03d
kind: topic
created: "2026-07-29"
updated: "2026-07-29"
status: active
sensitivity: normal
---
Bulk page.
`, id, index)
		if err := os.WriteFile(filepath.Join(service.Repo.Root, "pages", fmt.Sprintf("bulk-%03d.md", index)), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	principal := testPrincipal(t, "normal_reader", []auth.Permission{auth.PermissionQuery}, []string{"normal"})
	client := connectTestClient(t, service, principal)
	first, err := client.ListResources(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Resources) != 100 || first.NextCursor == "" || first.CacheScope != "private" {
		t.Fatalf("first resource page = count %d cursor %q cache %q", len(first.Resources), first.NextCursor, first.CacheScope)
	}
	second, err := client.ListResources(t.Context(), &mcp.ListResourcesParams{Cursor: first.NextCursor})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Resources) != 2 || second.NextCursor != "" || second.CacheScope != "private" {
		t.Fatalf("second resource page = count %d cursor %q cache %q", len(second.Resources), second.NextCursor, second.CacheScope)
	}
	if first.Resources[0].URI >= first.Resources[len(first.Resources)-1].URI ||
		first.Resources[len(first.Resources)-1].URI >= second.Resources[0].URI {
		t.Fatal("resource pages are not in deterministic URI order")
	}
}

func TestSearchReturnsExactResourceURI(t *testing.T) {
	service := newTestService(t)
	principal := testPrincipal(t, "normal_reader", []auth.Permission{auth.PermissionQuery}, []string{"normal"})
	client := connectTestClient(t, service, principal)
	output := decodeOutput[SearchOutput](t, callTool(t, client, "lore_search", map[string]any{
		"query":   "deployable",
		"backend": "filesystem",
	}))
	if len(output.Results) != 1 || output.Results[0].ResourceURI != "lore://pages/page_project_foo" {
		t.Fatalf("search resource URI = %+v", output.Results)
	}
	read := decodeOutput[ReadOutput](t, callTool(t, client, "lore_read", map[string]any{
		"ref": output.Results[0].ResourceURI,
	}))
	if read.ID != "page_project_foo" || !strings.Contains(read.Content, "Project Foo must remain deployable") {
		t.Fatalf("read search resource URI = %+v", read)
	}
}

func TestResourceListRefreshesAfterCommittedPageMutation(t *testing.T) {
	service := newTestService(t)
	var scans atomic.Int32
	scanner := func(ctx context.Context, repo *repository.Repository) (catalog.Catalog, error) {
		scans.Add(1)
		return scanResourceCatalog(ctx, repo)
	}
	client := connectClientToServer(t, newWithResourceScanner(t.Context(), service, fullPrincipal(t), nil, scanner))
	if scans.Load() != 1 {
		t.Fatalf("stdio construction scans = %d, want 1", scans.Load())
	}
	before, err := client.ListResources(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if scans.Load() != 1 {
		t.Fatalf("warm stdio resource list scans = %d, want 1", scans.Load())
	}
	preview := decodeOutput[PreviewOutput](t, callTool(t, client, "lore_preview", map[string]any{
		"schema_version": 1,
		"message":        "create: resource refresh",
		"operations": []any{map[string]any{
			"op":   "create_page",
			"path": "pages/resource-refresh.md",
			"content": `---
id: page_resource_refresh
title: Resource Refresh
kind: topic
created: "2026-07-29"
updated: "2026-07-29"
status: active
sensitivity: normal
---
Refresh resource listing.
`,
		}},
	}))
	_ = callTool(t, client, "lore_commit", map[string]any{
		"transaction_id": preview.TransactionID,
		"preview_digest": preview.PreviewDigest,
	})
	if scans.Load() != 2 {
		t.Fatalf("stdio post-commit resource scans = %d, want 2", scans.Load())
	}
	after, err := client.ListResources(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Resources) != len(before.Resources)+1 {
		t.Fatalf("resource count before=%d after=%d", len(before.Resources), len(after.Resources))
	}
	found := false
	for _, resource := range after.Resources {
		if resource.URI == "lore://pages/page_resource_refresh" {
			found = true
		}
	}
	if !found {
		t.Fatalf("committed page absent from resources: %+v", after.Resources)
	}
}

func httpQueryPrincipal(t *testing.T, id string, sensitivities []string) auth.Principal {
	t.Helper()
	principal, err := auth.NewPrincipal(id, auth.TransportHTTP, []auth.Permission{auth.PermissionQuery}, sensitivities)
	if err != nil {
		t.Fatal(err)
	}
	return principal
}
