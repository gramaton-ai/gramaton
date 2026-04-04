package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/brandonlattin/gramaton/config"
	"github.com/brandonlattin/gramaton/embed"
	"github.com/brandonlattin/gramaton/graph"
	"github.com/brandonlattin/gramaton/index"
	"github.com/brandonlattin/gramaton/search"
	"github.com/brandonlattin/gramaton/storage"
)

// engine holds the loaded state for a CLI command. Each command creates
// an engine, operates on it, and optionally saves. No long-lived server
// process needed for v0.1 -- each invocation loads, acts, saves.
type engine struct {
	cfg      config.Config
	store    *storage.Store
	graph    *graph.Graph
	propIdx  *index.PropertyIndex
	vecIdx   *index.FlatIndex
	embedder embed.Provider
	searcher *search.Tool
	headHash string
}

// loadEngine loads config, storage, graph state, and rebuilds indexes.
func loadEngine() (*engine, error) {
	dir := configDir()
	cfgPath := filepath.Join(dir, "config.yaml")

	cfg, err := config.Load(cfgPath)
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}

	if cfg.DataDir == "" {
		cfg.DataDir = filepath.Join(dir, "data")
	}

	s, err := storage.New(cfg.DataDir)
	if err != nil {
		return nil, fmt.Errorf("open storage: %w", err)
	}

	g := graph.New()
	propIdx := index.NewPropertyIndex()
	vecIdx := index.NewFlatIndex()

	// Load HEAD commit if it exists.
	var headHash string
	headPath := filepath.Join(cfg.DataDir, "HEAD")
	if data, err := os.ReadFile(headPath); err == nil {
		headHash = strings.TrimSpace(string(data))
		if headHash != "" {
			if _, err := g.Load(s, headHash); err != nil {
				return nil, fmt.Errorf("load HEAD commit: %w", err)
			}
		}
	}

	// Rebuild indexes from graph state.
	for _, id := range g.AllNodeIDs() {
		n, _ := g.GetNode(id)
		for k, v := range n.Properties {
			propIdx.Add(id, k, v)
		}
		// Rebuild vector index from embedding properties.
		for _, embKey := range []string{"embedding_keywords", "embedding_short", "embedding_abstract", "embedding_full"} {
			if v, ok := n.Properties[embKey]; ok {
				vecIdx.Add(id+":"+embKey, v.Vector())
			}
		}
	}

	// Create embedding provider (may be nil).
	emb, err := embed.New(cfg.Embedding)
	if err != nil {
		return nil, fmt.Errorf("create embedding provider: %w", err)
	}

	searcher := search.New(g, propIdx, vecIdx, emb, cfg)

	return &engine{
		cfg:      cfg,
		store:    s,
		graph:    g,
		propIdx:  propIdx,
		vecIdx:   vecIdx,
		embedder: emb,
		searcher: searcher,
		headHash: headHash,
	}, nil
}

// save commits the current graph state and updates HEAD.
func (e *engine) save(message string) (*graph.Commit, error) {
	commit, err := e.graph.Save(e.store, e.headHash, message)
	if err != nil {
		return nil, fmt.Errorf("save commit: %w", err)
	}

	headPath := filepath.Join(e.cfg.DataDir, "HEAD")
	if err := os.WriteFile(headPath, []byte(commit.Hash), 0o644); err != nil {
		return nil, fmt.Errorf("write HEAD: %w", err)
	}

	e.headHash = commit.Hash
	return commit, nil
}

// generateEmbeddings creates embeddings for a node's content properties
// and stores them as vector properties on the node.
func (e *engine) generateEmbeddings(ctx context.Context, nodeID string) error {
	if e.embedder == nil {
		return nil
	}

	n, ok := e.graph.GetNode(nodeID)
	if !ok {
		return fmt.Errorf("node %s not found", nodeID)
	}

	// Collect texts to embed from the summary pyramid.
	type embedTarget struct {
		sourceKey string
		embedKey  string
	}
	targets := []embedTarget{
		{"content_keywords", "embedding_keywords"},
		{"content_short", "embedding_short"},
		{"content_abstract", "embedding_abstract"},
		{"content_full", "embedding_full"},
	}

	var texts []string
	var keys []string
	for _, t := range targets {
		if v, ok := n.Properties[t.sourceKey]; ok {
			var text string
			if v.Type == graph.TypeStringList {
				text = strings.Join(v.StringList(), " ")
			} else if v.Type == graph.TypeString {
				text = v.String()
			}
			if text != "" {
				texts = append(texts, text)
				keys = append(keys, t.embedKey)
			}
		}
	}

	if len(texts) == 0 {
		return nil
	}

	vectors, err := e.embedder.Embed(ctx, texts)
	if err != nil {
		return fmt.Errorf("generate embeddings: %w", err)
	}

	for i, vec := range vectors {
		prop := graph.VectorProperty(vec)
		e.graph.SetNodeProperty(nodeID, keys[i], prop)
		e.propIdx.Add(nodeID, keys[i], prop)
		e.vecIdx.Add(nodeID+":"+keys[i], vec)
	}

	// Track which model generated these embeddings.
	modelProp := graph.StringProperty(e.embedder.ModelID())
	e.graph.SetNodeProperty(nodeID, "embedding_model", modelProp)
	e.propIdx.Add(nodeID, "embedding_model", modelProp)

	return nil
}
