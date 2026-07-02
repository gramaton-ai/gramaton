package server

import (
	"context"
	"log/slog"
	"net/http"
	"testing"

	"github.com/gramaton-ai/gramaton/config"
	"github.com/gramaton-ai/gramaton/core"
	"github.com/gramaton-ai/gramaton/index"
)

// The composed identity the author-configured test server stamps.
const wantServerAuthor = "Ada Lovelace <ada@example.com>"

// setupAuthorServer mirrors setupTestServer but gives the engine an
// author identity, for exercising the server-side stamping paths
// (intake save, ingest).
func setupAuthorServer(t *testing.T) (*Server, *core.Engine) {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.DataDir = dir
	cfg.Embedding.Provider = ""
	cfg.LLM.Provider = ""
	cfg.Backup.Dir = t.TempDir() + "/backups"
	cfg.Author = config.AuthorConfig{Name: "Ada Lovelace", Email: "ada@example.com"}
	if err := config.Save(cfg, dir+"/config.yaml"); err != nil {
		t.Fatalf("config.Save: %v", err)
	}

	eng, err := core.LoadEngineWithOptions(dir, nil, []core.EngineOption{
		core.WithLLM(noopLLM{}),
		core.WithVectorIndex(index.NewFlatIndex()),
		core.WithVolatileStorage(),
	})
	if err != nil {
		t.Fatalf("LoadEngine: %v", err)
	}
	t.Cleanup(func() { eng.Close() })

	serverCfg := DefaultConfig()
	serverCfg.ConfigDir = dir
	srv, err := New(eng, serverCfg, slog.Default())
	if err != nil {
		t.Fatalf("New server: %v", err)
	}
	return srv, eng
}

func assertServerAuthor(t *testing.T, eng *core.Engine, id, context string) {
	t.Helper()
	eng.RLock()
	defer eng.RUnlock()
	n, ok := eng.Graph().GetNode(id)
	if !ok {
		t.Fatalf("%s: node %s not found", context, id)
	}
	got, has := n.Properties.GetString("author")
	if !has {
		t.Fatalf("%s: node %s has no author property, want %q", context, id, wantServerAuthor)
	}
	if got != wantServerAuthor {
		t.Errorf("%s: author = %q, want %q", context, got, wantServerAuthor)
	}
}

// TestServiceSaveStampsAuthor: the server-side save service (backing
// both the intake path and the legacy HTTP save) stamps the composed
// config identity.
func TestServiceSaveStampsAuthor(t *testing.T) {
	srv, eng := setupAuthorServer(t)
	result, svcErr := srv.serviceSave(context.Background(), &saveRequest{
		Content: "server intake author stamp",
	})
	if svcErr != nil {
		t.Fatalf("serviceSave: %v", svcErr)
	}
	assertServerAuthor(t, eng, result["id"].(string), "serviceSave")
}

// TestIngestStampsAuthor: /v1/ingest stamps every ingested file's
// record with the composed config identity (composed once per
// request).
func TestIngestStampsAuthor(t *testing.T) {
	srv, eng := setupAuthorServer(t)
	w := doRequest(t, srv, "POST", "/v1/ingest", map[string]any{
		"files": []map[string]any{
			{"filename": "one.md", "content": "# First ingested file"},
			{"filename": "two.md", "content": "# Second ingested file"},
		},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	eng.RLock()
	ids := eng.Graph().AllNodeIDs()
	eng.RUnlock()
	if len(ids) != 2 {
		t.Fatalf("expected 2 ingested nodes, got %d", len(ids))
	}
	for _, id := range ids {
		assertServerAuthor(t, eng, id, "ingest")
	}
}
