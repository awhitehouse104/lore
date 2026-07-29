package index

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"lore/internal/gitx"
	"lore/internal/search"
)

type failHooks struct {
	buildErr  error
	updateErr error
}

func (h failHooks) BeforeBuildReplace() error {
	return h.buildErr
}

func (h failHooks) BeforeUpdateCommit() error {
	return h.updateErr
}

type pauseHooks struct {
	buildEntered  chan struct{}
	buildRelease  chan struct{}
	updateEntered chan struct{}
	updateRelease chan struct{}
}

func (h pauseHooks) BeforeBuildReplace() error {
	if h.buildEntered == nil {
		return nil
	}
	h.buildEntered <- struct{}{}
	<-h.buildRelease
	return nil
}

func (h pauseHooks) BeforeUpdateCommit() error {
	if h.updateEntered == nil {
		return nil
	}
	h.updateEntered <- struct{}{}
	<-h.updateRelease
	return nil
}

func TestBuildHookFailurePreservesPriorIndexExactly(t *testing.T) {
	ctx := context.Background()
	repo := newTestRepository(t)
	writeTestPage(t, repo.Root, "indexed body")
	manager := NewManager(repo, gitx.New(), "0.3.0-test")
	if _, err := manager.Build(ctx, BuildOptions{}); err != nil {
		t.Fatal(err)
	}
	indexPath := filepath.Join(repo.Root, filepath.FromSlash(RelativeIndexPath))
	before, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}

	manager.Hooks = failHooks{buildErr: errors.New("injected failure")}
	_, err = manager.Build(ctx, BuildOptions{Force: true})
	var indexErr *Error
	if !errors.As(err, &indexErr) || indexErr.Code != "index_build_interrupted" {
		t.Fatalf("Build error = %T %v", err, err)
	}
	after, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("interrupted build changed the prior index bytes")
	}
	manager.Hooks = nil
	status, err := manager.Status(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if status.IndexState != StateUncertified || !status.ManifestMatches {
		t.Fatalf("prior index is not usable: %+v", status)
	}
}

func TestUpdateHookFailureRollsBackIndexTransaction(t *testing.T) {
	ctx := context.Background()
	repo := newTestRepository(t)
	writeTestPage(t, repo.Root, "old body")
	manager := NewManager(repo, gitx.New(), "0.3.0-test")
	if _, err := manager.Build(ctx, BuildOptions{}); err != nil {
		t.Fatal(err)
	}
	writeTestPage(t, repo.Root, "new body")
	manager.Hooks = failHooks{updateErr: errors.New("injected failure")}
	_, err := manager.Update(ctx)
	var indexErr *Error
	if !errors.As(err, &indexErr) || indexErr.Code != "index_update_interrupted" {
		t.Fatalf("Update error = %T %v", err, err)
	}
	assertIndexedBody(t, repo.Root, "pages/index-test.md", "old body\n")

	manager.Hooks = nil
	if _, err := manager.Update(ctx); err != nil {
		t.Fatalf("Update after rollback: %v", err)
	}
	assertIndexedBody(t, repo.Root, "pages/index-test.md", "new body\n")
}

func TestIndexedSearchContinuesDuringPausedNoOpUpdate(t *testing.T) {
	ctx := context.Background()
	repo := newTestRepository(t)
	writeTestPage(t, repo.Root, "indexed body")
	manager := NewManager(repo, gitx.New(), "0.3.0-test")
	if _, err := manager.Build(ctx, BuildOptions{}); err != nil {
		t.Fatal(err)
	}
	hooks := pauseHooks{
		updateEntered: make(chan struct{}, 1),
		updateRelease: make(chan struct{}),
	}
	manager.Hooks = hooks
	updateDone := make(chan error, 1)
	go func() {
		_, err := manager.Update(ctx)
		updateDone <- err
	}()
	waitForSignal(t, hooks.updateEntered, "update hook")

	hybrid := search.HybridSearcher{
		Filesystem:          search.FilesystemLexicalSearcher{},
		Index:               manager,
		CandidateMultiplier: 20,
		MinimumCandidates:   20,
		MaximumCandidates:   200,
	}
	response, err := hybrid.SearchDetailed(ctx, repo, search.Query{
		Text: "indexed", Scope: search.ScopePages, Limit: 10,
		Backend: search.BackendIndex, Access: search.AllAccessPolicy(),
	})
	if err != nil {
		close(hooks.updateRelease)
		t.Fatalf("SearchDetailed during update: %v", err)
	}
	if response.Backend != search.BackendIndex || len(response.Results) != 1 {
		close(hooks.updateRelease)
		t.Fatalf("search response = %+v", response)
	}
	close(hooks.updateRelease)
	waitForError(t, updateDone, "update")
}

func TestExclusiveBuildReportsBuildingAndBlocksClear(t *testing.T) {
	ctx := context.Background()
	repo := newTestRepository(t)
	writeTestPage(t, repo.Root, "indexed body")
	manager := NewManager(repo, gitx.New(), "0.3.0-test")
	if _, err := manager.Build(ctx, BuildOptions{}); err != nil {
		t.Fatal(err)
	}
	hooks := pauseHooks{
		buildEntered: make(chan struct{}, 1),
		buildRelease: make(chan struct{}),
	}
	manager.Hooks = hooks
	buildDone := make(chan error, 1)
	go func() {
		_, err := manager.Build(ctx, BuildOptions{Force: true})
		buildDone <- err
	}()
	waitForSignal(t, hooks.buildEntered, "build hook")

	status, err := manager.Status(ctx, false)
	if err != nil {
		close(hooks.buildRelease)
		t.Fatal(err)
	}
	if status.IndexState != StateBuilding {
		close(hooks.buildRelease)
		t.Fatalf("status during build = %+v", status)
	}
	_, err = manager.Clear()
	var indexErr *Error
	if !errors.As(err, &indexErr) || indexErr.Code != "index_busy" {
		close(hooks.buildRelease)
		t.Fatalf("Clear error during build = %T %v", err, err)
	}
	close(hooks.buildRelease)
	waitForError(t, buildDone, "build")
}

func waitForSignal(t *testing.T, channel <-chan struct{}, operation string) {
	t.Helper()
	select {
	case <-channel:
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for %s", operation)
	}
}

func waitForError(t *testing.T, channel <-chan error, operation string) {
	t.Helper()
	select {
	case err := <-channel:
		if err != nil {
			t.Fatalf("%s failed: %v", operation, err)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for %s", operation)
	}
}
