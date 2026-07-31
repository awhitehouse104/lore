package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"lore/internal/auth"
	"lore/internal/catalog"
	"lore/internal/core"
	"lore/internal/docs"
	"lore/internal/repository"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	resourceMIMEType     = "text/markdown; charset=utf-8"
	resourceListMethod   = "resources/list"
	maximumResourceBytes = 512 * 1024
)

var errResourceCatalogUnavailable = errors.New("Lore resource catalog unavailable")

type resourceCatalogScanner func(context.Context, *repository.Repository) (catalog.Catalog, error)

func scanResourceCatalog(ctx context.Context, repo *repository.Repository) (catalog.Catalog, error) {
	documentCatalog, _, err := catalog.Scan(ctx, repo, false)
	return documentCatalog, err
}

func (s *Server) addResources(ctx context.Context) {
	if !s.principal.Has(auth.PermissionQuery) {
		return
	}
	s.mcp.AddReceivingMiddleware(s.lazyResourceListMiddleware)
	for _, kind := range []string{"pages", "sources"} {
		s.mcp.AddResourceTemplate(&mcp.ResourceTemplate{
			URITemplate: "lore://" + kind + "/{id}",
			Name:        "Lore " + strings.TrimSuffix(kind, "s") + " by canonical ID",
			Description: "Read one authorized Lore document by its exact canonical ID",
			MIMEType:    resourceMIMEType,
		}, s.readResource)
	}
	if s.principal.Transport != auth.TransportHTTP {
		if err := s.ensurePageResources(ctx); err != nil {
			s.logResourceCatalogUnavailable()
		}
	}
}

type pageResource struct {
	name        string
	title       string
	description string
	size        int64
}

func (s *Server) lazyResourceListMiddleware(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, request mcp.Request) (mcp.Result, error) {
		if method == resourceListMethod {
			if err := s.ensurePageResources(ctx); err != nil {
				s.logResourceCatalogUnavailable()
				return nil, errResourceCatalogUnavailable
			}
		}
		return next(ctx, method, request)
	}
}

func (s *Server) ensurePageResources(ctx context.Context) error {
	s.resourceMu.Lock()
	defer s.resourceMu.Unlock()
	if s.resourcesLoaded {
		return nil
	}
	return s.reloadPageResourcesLocked(ctx)
}

func (s *Server) refreshPageResources(ctx context.Context) {
	s.resourceMu.Lock()
	defer s.resourceMu.Unlock()
	if !s.resourcesLoaded {
		return
	}
	if err := s.reloadPageResourcesLocked(ctx); err != nil {
		s.logResourceCatalogUnavailable()
	}
}

func (s *Server) reloadPageResourcesLocked(ctx context.Context) error {
	documentCatalog, err := s.resourceScan(ctx, s.service.Repo)
	if err != nil {
		s.resourcesLoaded = false
		return err
	}
	desired := make(map[string]pageResource)
	for _, document := range documentCatalog.Documents {
		if document.Page == nil || !s.principal.AllowsSensitivity(document.Sensitivity()) {
			continue
		}
		uri := canonicalResourceURI("pages", document.ID())
		desired[uri] = pageResource{
			name:        document.ID(),
			title:       document.Title(),
			description: fmt.Sprintf("Authorized Lore %s page", document.Kind()),
			size:        int64(len(document.Data)),
		}
	}
	for uri := range s.pageResources {
		if _, exists := desired[uri]; !exists {
			s.mcp.RemoveResources(uri)
		}
	}
	for uri, resource := range desired {
		if current, exists := s.pageResources[uri]; exists && current == resource {
			continue
		}
		s.mcp.AddResource(&mcp.Resource{
			URI:         uri,
			Name:        resource.name,
			Title:       resource.title,
			Description: resource.description,
			MIMEType:    resourceMIMEType,
			Size:        resource.size,
		}, s.readResource)
	}
	s.pageResources = desired
	s.resourcesLoaded = true
	return nil
}

func (s *Server) logResourceCatalogUnavailable() {
	s.logger.Error("Lore resource catalog unavailable", "code", "catalog_scan_failed")
}

func (s *Server) readResource(ctx context.Context, request *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	if !s.principal.Has(auth.PermissionQuery) {
		return nil, mcp.ResourceNotFoundError(request.Params.URI)
	}
	id, err := parseCanonicalResourceURI(request.Params.URI)
	if err != nil {
		return nil, mcp.ResourceNotFoundError(request.Params.URI)
	}
	result, err := s.service.ReadAuthorized(ctx, id, nil, s.principal.AccessPolicy())
	if err != nil {
		var apiErr *core.APIError
		if errors.As(err, &apiErr) && (apiErr.Code == "reference_not_found" || apiErr.Code == "unsafe_reference") {
			return nil, mcp.ResourceNotFoundError(request.Params.URI)
		}
		return nil, fmt.Errorf("Lore resource read failed")
	}
	if len(result.Content) > maximumResourceBytes {
		return nil, fmt.Errorf("Lore resource exceeds the bounded read limit; use lore_read with a line range")
	}
	return &mcp.ReadResourceResult{
		Cacheable: mcp.Cacheable{TTLMs: 0, CacheScope: "private"},
		Contents: []*mcp.ResourceContents{{
			URI:      request.Params.URI,
			MIMEType: resourceMIMEType,
			Text:     result.Content,
			Meta: mcp.Meta{
				"lore/revision":    result.Revision,
				"lore/document_id": result.ID,
			},
		}},
	}, nil
}

func canonicalResourceURI(kind, id string) string {
	return "lore://" + kind + "/" + url.PathEscape(id)
}

func parseCanonicalResourceURI(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "lore" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.Fragment != "" ||
		(parsed.Host != "pages" && parsed.Host != "sources") {
		return "", fmt.Errorf("invalid Lore resource URI")
	}
	escaped := strings.TrimPrefix(parsed.EscapedPath(), "/")
	if escaped == "" || strings.Contains(escaped, "/") {
		return "", fmt.Errorf("invalid Lore resource URI")
	}
	id, err := url.PathUnescape(escaped)
	if err != nil || escaped != url.PathEscape(id) {
		return "", fmt.Errorf("invalid Lore resource URI")
	}
	if parsed.Host == "pages" {
		err = docs.ValidatePageID(id)
	} else {
		err = docs.ValidateSourceID(id)
	}
	if err != nil {
		return "", fmt.Errorf("invalid Lore resource URI")
	}
	return id, nil
}

func privateCacheMiddleware(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, request mcp.Request) (mcp.Result, error) {
		result, err := next(ctx, method, request)
		if err != nil {
			return result, err
		}
		cacheable := mcp.Cacheable{TTLMs: 0, CacheScope: "private"}
		switch typed := result.(type) {
		case *mcp.DiscoverResult:
			typed.Cacheable = cacheable
		case *mcp.ListToolsResult:
			typed.Cacheable = cacheable
		case *mcp.ListPromptsResult:
			typed.Cacheable = cacheable
		case *mcp.ListResourcesResult:
			typed.Cacheable = cacheable
		case *mcp.ListResourceTemplatesResult:
			typed.Cacheable = cacheable
		case *mcp.ReadResourceResult:
			typed.Cacheable = cacheable
		}
		return result, nil
	}
}
