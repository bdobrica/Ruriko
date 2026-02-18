package store_test

import (
	"context"
	"testing"

	"github.com/bdobrica/Ruriko/internal/ruriko/store"
)

func makeTestAgent(t *testing.T, s *store.Store, id string) {
	t.Helper()
	if err := s.CreateAgent(context.Background(), &store.Agent{
		ID:          id,
		DisplayName: id,
		Template:    "cron",
		Status:      "stopped",
	}); err != nil {
		t.Fatalf("createTestAgent %q: %v", id, err)
	}
}

func TestCreateAndGetGosutoVersion(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	makeTestAgent(t, s, "agent1")

	gv := &store.GosutoVersion{
		AgentID:       "agent1",
		Version:       1,
		Hash:          "abc123",
		YAMLBlob:      "apiVersion: gosuto/v1\n",
		CreatedByMXID: "@alice:example.com",
	}

	if err := s.CreateGosutoVersion(ctx, gv); err != nil {
		t.Fatalf("CreateGosutoVersion: %v", err)
	}

	got, err := s.GetGosutoVersion(ctx, "agent1", 1)
	if err != nil {
		t.Fatalf("GetGosutoVersion: %v", err)
	}

	if got.Hash != "abc123" {
		t.Errorf("Hash: got %q, want %q", got.Hash, "abc123")
	}
	if got.YAMLBlob != "apiVersion: gosuto/v1\n" {
		t.Errorf("YAMLBlob: got %q, want ...", got.YAMLBlob)
	}
	if got.CreatedByMXID != "@alice:example.com" {
		t.Errorf("CreatedByMXID: got %q, want %q", got.CreatedByMXID, "@alice:example.com")
	}
}

func TestGetGosutoVersion_NotFound(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	makeTestAgent(t, s, "agent1")

	_, err := s.GetGosutoVersion(ctx, "agent1", 99)
	if err == nil {
		t.Fatal("expected error for missing version, got nil")
	}
}

func TestGetLatestGosutoVersion(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	makeTestAgent(t, s, "agent1")

	for _, ver := range []int{1, 2, 3} {
		if err := s.CreateGosutoVersion(ctx, &store.GosutoVersion{
			AgentID:       "agent1",
			Version:       ver,
			Hash:          "hash" + string(rune('0'+ver)),
			YAMLBlob:      "yaml",
			CreatedByMXID: "@alice:example.com",
		}); err != nil {
			t.Fatalf("CreateGosutoVersion v%d: %v", ver, err)
		}
	}

	latest, err := s.GetLatestGosutoVersion(ctx, "agent1")
	if err != nil {
		t.Fatalf("GetLatestGosutoVersion: %v", err)
	}

	if latest.Version != 3 {
		t.Errorf("latest version: got %d, want 3", latest.Version)
	}
}

func TestGetLatestGosutoVersion_NoVersions(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	makeTestAgent(t, s, "agent1")

	_, err := s.GetLatestGosutoVersion(ctx, "agent1")
	if err == nil {
		t.Fatal("expected error when no versions exist, got nil")
	}
}

func TestNextGosutoVersion(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	makeTestAgent(t, s, "agent1")

	// No versions yet → should return 1.
	v, err := s.NextGosutoVersion(ctx, "agent1")
	if err != nil {
		t.Fatalf("NextGosutoVersion (empty): %v", err)
	}
	if v != 1 {
		t.Errorf("next version (empty): got %d, want 1", v)
	}

	// Create v1.
	if err := s.CreateGosutoVersion(ctx, &store.GosutoVersion{
		AgentID:       "agent1",
		Version:       1,
		Hash:          "h1",
		YAMLBlob:      "y",
		CreatedByMXID: "@alice:example.com",
	}); err != nil {
		t.Fatalf("CreateGosutoVersion: %v", err)
	}

	v, err = s.NextGosutoVersion(ctx, "agent1")
	if err != nil {
		t.Fatalf("NextGosutoVersion (after v1): %v", err)
	}
	if v != 2 {
		t.Errorf("next version (after v1): got %d, want 2", v)
	}
}

func TestListGosutoVersions(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	makeTestAgent(t, s, "agent1")

	for _, ver := range []int{1, 2, 3} {
		if err := s.CreateGosutoVersion(ctx, &store.GosutoVersion{
			AgentID:       "agent1",
			Version:       ver,
			Hash:          "h",
			YAMLBlob:      "y",
			CreatedByMXID: "@alice:example.com",
		}); err != nil {
			t.Fatalf("CreateGosutoVersion v%d: %v", ver, err)
		}
	}

	versions, err := s.ListGosutoVersions(ctx, "agent1")
	if err != nil {
		t.Fatalf("ListGosutoVersions: %v", err)
	}

	if len(versions) != 3 {
		t.Errorf("version count: got %d, want 3", len(versions))
	}

	// Should be newest-first.
	if versions[0].Version != 3 {
		t.Errorf("first entry should be v3, got v%d", versions[0].Version)
	}
}

func TestPruneGosutoVersions(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	makeTestAgent(t, s, "agent1")

	for ver := 1; ver <= 5; ver++ {
		if err := s.CreateGosutoVersion(ctx, &store.GosutoVersion{
			AgentID:       "agent1",
			Version:       ver,
			Hash:          "h",
			YAMLBlob:      "y",
			CreatedByMXID: "@alice:example.com",
		}); err != nil {
			t.Fatalf("CreateGosutoVersion v%d: %v", ver, err)
		}
	}

	if err := s.PruneGosutoVersions(ctx, "agent1", 3); err != nil {
		t.Fatalf("PruneGosutoVersions: %v", err)
	}

	versions, err := s.ListGosutoVersions(ctx, "agent1")
	if err != nil {
		t.Fatalf("ListGosutoVersions after prune: %v", err)
	}

	if len(versions) != 3 {
		t.Errorf("after prune (keep 3): got %d versions, want 3", len(versions))
	}

	// Should retain the 3 most recent: v5, v4, v3.
	for i, want := range []int{5, 4, 3} {
		if versions[i].Version != want {
			t.Errorf("versions[%d].Version: got %d, want %d", i, versions[i].Version, want)
		}
	}
}

func TestGosutoVersion_AgentVersionUpdated(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	makeTestAgent(t, s, "agent1")

	if err := s.CreateGosutoVersion(ctx, &store.GosutoVersion{
		AgentID:       "agent1",
		Version:       1,
		Hash:          "h1",
		YAMLBlob:      "y",
		CreatedByMXID: "@alice:example.com",
	}); err != nil {
		t.Fatalf("CreateGosutoVersion: %v", err)
	}

	agent, err := s.GetAgent(ctx, "agent1")
	if err != nil {
		t.Fatalf("GetAgent: %v", err)
	}

	if !agent.GosutoVersion.Valid || agent.GosutoVersion.Int64 != 1 {
		t.Errorf("agent.GosutoVersion: got %v/%v, want 1", agent.GosutoVersion.Valid, agent.GosutoVersion.Int64)
	}
}
