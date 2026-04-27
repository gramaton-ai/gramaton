package config

import (
	"bytes"
	"fmt"

	"gopkg.in/yaml.v3"
)

// renderConfig serializes cfg to YAML with section banners and per-field
// descriptions injected as YAML comments. Used by Save() so the file an
// operator opens in an editor is self-explanatory: every dial under llm:
// has a comment above it.
//
// Implementation: encode the Config to a yaml.Node tree (which already
// has the right shape -- yaml tags applied, omitempty respected, time.
// Duration formatted), walk the tree attaching HeadComment to mapping
// keys from commentRegistry (keyed by yaml-path), then re-marshal.
//
// The walker also reorders the llm.models.tasks map to a stable order
// (classification_short -> classification_long -> summarization ->
// contradiction -> concept -> manifest -> rerank -> decompose) so the
// rendered file is deterministic across saves and matches the order
// users see when reading docs. yaml.v3 alphabetizes string-keyed maps
// by default, which puts the entries in an order that doesn't track
// the LLMTask logical sequence.
func renderConfig(cfg Config) ([]byte, error) {
	var root yaml.Node
	if err := root.Encode(cfg); err != nil {
		return nil, fmt.Errorf("config: encode to node: %w", err)
	}

	walkAndComment(&root, "")

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(yamlIndent)
	if err := enc.Encode(&root); err != nil {
		return nil, fmt.Errorf("config: marshal node: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("config: close encoder: %w", err)
	}
	return buf.Bytes(), nil
}

// yamlIndent matches the indent the project ships in existing config.
// yaml files (4-space). Two-space would also parse but reads cramped
// for nested blocks like llm.curation.contradiction.*.
const yamlIndent = 4

// walkAndComment recurses through a yaml.Node tree attaching
// HeadComment text from commentRegistry to mapping-key nodes. path
// accumulates the dot-joined yaml-path used as the registry key.
func walkAndComment(node *yaml.Node, path string) {
	switch node.Kind {
	case yaml.DocumentNode:
		for _, child := range node.Content {
			walkAndComment(child, path)
		}
	case yaml.MappingNode:
		if path == "llm.models.tasks" {
			reorderTasksMap(node)
		}
		for i := 0; i+1 < len(node.Content); i += 2 {
			keyNode := node.Content[i]
			valNode := node.Content[i+1]
			childPath := keyNode.Value
			if path != "" {
				childPath = path + "." + keyNode.Value
			}
			if comment, ok := commentRegistry[childPath]; ok {
				keyNode.HeadComment = comment
			}
			walkAndComment(valNode, childPath)
		}
	}
	// Sequences: comments don't make sense inside arrays in our
	// schema (no array-of-struct fields under llm:). Scalars: nothing
	// to recurse into.
}

// taskRenderOrder is the canonical order for llm.models.tasks entries.
// Tracks the LLMTask declaration order in config.go: classification
// pair first, then content-mutation tasks, then maintenance, then
// search-time. Operators reading the file get a logical sequence
// instead of alphabetical noise.
var taskRenderOrder = []string{
	"classification_short",
	"classification_long",
	"summarization",
	"contradiction",
	"concept",
	"manifest",
	"rerank",
	"decompose",
}

// reorderTasksMap rearranges the llm.models.tasks mapping node in
// place to match taskRenderOrder. Unknown keys (none expected with
// the closed LLMTask enum, but possible if a user adds a custom
// entry in config.yaml) preserve their relative order at the end.
func reorderTasksMap(node *yaml.Node) {
	if node.Kind != yaml.MappingNode {
		return
	}
	type pair struct {
		key, val *yaml.Node
	}
	indexed := make(map[string]pair, len(node.Content)/2)
	for i := 0; i+1 < len(node.Content); i += 2 {
		indexed[node.Content[i].Value] = pair{node.Content[i], node.Content[i+1]}
	}
	seen := make(map[string]bool, len(taskRenderOrder))
	out := make([]*yaml.Node, 0, len(node.Content))
	for _, name := range taskRenderOrder {
		if p, ok := indexed[name]; ok {
			out = append(out, p.key, p.val)
			seen[name] = true
		}
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if !seen[node.Content[i].Value] {
			out = append(out, node.Content[i], node.Content[i+1])
		}
	}
	node.Content = out
}
