package manifest

import "testing"

func TestParseValid(t *testing.T) {
	doc := `{
	  "registry": {"scope": "jmlago"},
	  "packages": [
	    {"name": "web-researcher", "dir": "packages/web-researcher", "kind": "body", "description": "Web research agent body"},
	    {"name": "cost-router", "dir": "packages/cost-router", "kind": "policy"},
	    {"name": "task-board", "dir": "packages/task-board", "kind": "handler", "deps": ["jmlago/kv-store@^0.3"]},
	    {"name": "research-swarm", "dir": "packages/research-swarm", "kind": "swarm"}
	  ]
	}`
	m, err := Parse([]byte(doc))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if m.Registry.Scope != "jmlago" || len(m.Packages) != 4 {
		t.Fatalf("unexpected manifest: %+v", m)
	}
}

func TestParseRejects(t *testing.T) {
	cases := map[string]string{
		"missing scope": `{"packages":[{"name":"a","dir":"d","kind":"body"}]}`,
		"invalid kind":  `{"registry":{"scope":"jmlago"},"packages":[{"name":"a","dir":"d","kind":"widget"}]}`,
		"missing dir":   `{"registry":{"scope":"jmlago"},"packages":[{"name":"a","kind":"body"}]}`,
		"dup name":      `{"registry":{"scope":"jmlago"},"packages":[{"name":"a","dir":"d","kind":"body"},{"name":"a","dir":"e","kind":"policy"}]}`,
		"version field": `{"registry":{"scope":"jmlago"},"packages":[{"name":"a","dir":"d","kind":"body","version":"1.0.0"}]}`,
	}
	for name, doc := range cases {
		if _, err := Parse([]byte(doc)); err == nil {
			t.Errorf("%s: expected error, got none", name)
		}
	}
}
