// Package manifest parses and validates swarmidx.json (design §7): the repo-root
// manifest where each package's `kind` is its slot role, the scope must match the
// GitHub owner, and there is deliberately NO per-package version (the tag is the
// version, §8).
package manifest

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// Manifest is a swarmidx.json document.
type Manifest struct {
	Registry Registry  `json:"registry"`
	Packages []Package `json:"packages"`
}

// Registry carries the scope (must equal the GitHub owner, verified at publish).
type Registry struct {
	Scope string `json:"scope"`
}

// Package is one entry: name + dir (whose content is hashed) + kind (slot role).
// Docs/Skill are display metadata for the registry card: a path relative to the
// source root (e.g. "docs/guide.md") or an absolute URL. When absent the notary
// falls back to README.md / SKILL.md detected in the package dir.
type Package struct {
	Name        string   `json:"name"`
	Dir         string   `json:"dir"`
	Kind        string   `json:"kind"`
	Description string   `json:"description,omitempty"`
	Note        string   `json:"note,omitempty"`
	Docs        string   `json:"docs,omitempty"`
	Skill       string   `json:"skill,omitempty"`
	Deps        []string `json:"deps,omitempty"`
}

// ValidKinds are the slot roles a package may declare (§6).
var ValidKinds = map[string]bool{"body": true, "policy": true, "handler": true, "swarm": true}

// Parse parses and validates a swarmidx.json document.
func Parse(data []byte) (Manifest, error) {
	var m Manifest
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return Manifest{}, fmt.Errorf("swarmidx.json: %w", err)
	}
	if err := m.Validate(); err != nil {
		return Manifest{}, err
	}
	return m, nil
}

// Validate enforces the §7 rules: a scope, and each package with a name, a dir,
// and a known kind. Names must be unique within the manifest.
func (m Manifest) Validate() error {
	if m.Registry.Scope == "" {
		return fmt.Errorf("missing registry.scope")
	}
	seen := map[string]bool{}
	for i, p := range m.Packages {
		if p.Name == "" {
			return fmt.Errorf("package %d: missing name", i)
		}
		if p.Dir == "" {
			return fmt.Errorf("package %q: missing dir", p.Name)
		}
		if !ValidKinds[p.Kind] {
			return fmt.Errorf("package %q: invalid kind %q", p.Name, p.Kind)
		}
		if seen[p.Name] {
			return fmt.Errorf("duplicate package name: %q", p.Name)
		}
		seen[p.Name] = true
	}
	return nil
}
