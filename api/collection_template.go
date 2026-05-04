package api

import (
	"embed"
	"fmt"
	"sort"
	"sync"

	"gopkg.in/yaml.v3"
)

//go:embed templates/*.yaml
var embeddedTemplates embed.FS

// Template is a reusable collection shape -- schema + behaviour
// knobs -- that CollectionCreate can instantiate by name. Templates
// ship embedded in the binary so fresh stores have them available
// without extra provisioning, and so callers can name them without
// worrying about filesystem paths.
//
// Template fields mirror the fields on CollectionCreateRequest so
// the merge at CollectionCreate time is a plain "non-empty wins"
// shallow merge -- whatever the caller passes explicitly overrides
// the template's default.
type Template struct {
	Name           string            `yaml:"name"`
	Description    string            `yaml:"description,omitempty"`
	Schema         *CollectionSchema `yaml:"schema,omitempty"`
	ClearMode      string            `yaml:"clear_mode,omitempty"`
	Supersession   string            `yaml:"supersession,omitempty"`
	Curation       string            `yaml:"curation,omitempty"`
	Contradictions string            `yaml:"contradictions,omitempty"`
}

var (
	tmplRegistryOnce sync.Once
	tmplRegistry     map[string]*Template
	tmplRegistryErr  error
)

// loadTemplates parses every YAML file under the embedded
// templates/ directory into the registry. Called lazily so server
// startup doesn't pay the cost when no caller passes a template.
func loadTemplates() {
	registry := make(map[string]*Template)
	entries, err := embeddedTemplates.ReadDir("templates")
	if err != nil {
		tmplRegistryErr = fmt.Errorf("read templates dir: %w", err)
		return
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		data, err := embeddedTemplates.ReadFile("templates/" + entry.Name())
		if err != nil {
			tmplRegistryErr = fmt.Errorf("read template %s: %w", entry.Name(), err)
			return
		}
		var t Template
		if err := yaml.Unmarshal(data, &t); err != nil {
			tmplRegistryErr = fmt.Errorf("parse template %s: %w", entry.Name(), err)
			return
		}
		if t.Name == "" {
			tmplRegistryErr = fmt.Errorf("template %s has no name", entry.Name())
			return
		}
		if _, dup := registry[t.Name]; dup {
			tmplRegistryErr = fmt.Errorf("duplicate template name %q (file %s)", t.Name, entry.Name())
			return
		}
		registry[t.Name] = &t
	}
	tmplRegistry = registry
}

// LookupTemplate returns a named template from the embedded
// registry. The second return is false when no template matches.
// Lazy-initialises the registry on first call; subsequent calls are
// a bare map lookup.
func LookupTemplate(name string) (*Template, bool) {
	tmplRegistryOnce.Do(loadTemplates)
	if tmplRegistryErr != nil {
		return nil, false
	}
	t, ok := tmplRegistry[name]
	return t, ok
}

// ListTemplates returns the names of all registered templates
// sorted alphabetically. Useful for a future "gramaton template
// list" CLI or the collection-creation wizard.
func ListTemplates() []string {
	tmplRegistryOnce.Do(loadTemplates)
	if tmplRegistryErr != nil {
		return nil
	}
	names := make([]string, 0, len(tmplRegistry))
	for name := range tmplRegistry {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// applyTemplate merges a template's defaults into a
// CollectionCreateRequest. Caller-supplied non-empty fields always
// win; empty fields inherit from the template. Returns ErrInvalid
// if the named template doesn't exist.
func applyTemplate(req *CollectionCreateRequest) *APIError {
	if req.Template == "" {
		return nil
	}
	t, ok := LookupTemplate(req.Template)
	if !ok {
		return ErrInvalid(fmt.Sprintf("template %q not found; available: %v", req.Template, ListTemplates()))
	}
	if req.Description == "" {
		req.Description = t.Description
	}
	if req.Schema == nil {
		req.Schema = t.Schema
	}
	if req.ClearMode == "" {
		req.ClearMode = t.ClearMode
	}
	if req.Supersession == "" {
		req.Supersession = t.Supersession
	}
	if req.Curation == "" {
		req.Curation = t.Curation
	}
	if req.Contradictions == "" {
		req.Contradictions = t.Contradictions
	}
	return nil
}
