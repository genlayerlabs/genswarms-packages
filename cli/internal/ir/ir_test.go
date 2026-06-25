package ir

import "testing"

const seedJSON = `{
  "v": 1, "kind": "swarm.state", "name": "research", "phase": "desired",
  "agents": [
    {
      "name": "coder",
      "body": {"ref": "swarmidx:jmlago/coder@0.4.0", "kind": "data", "digest": "sha256:3a8f1c"},
      "model": {"ref": "openrouter:anthropic/claude-sonnet-4", "attested": true},
      "backend": {"ref": "bwrap"}
    },
    {
      "name": "researcher",
      "body": {"ref": "swarmidx:jmlago/web-researcher@1.2.3", "kind": "data", "digest": "sha256:aaaa"},
      "model": {"policy": {"ref": "swarmidx:jmlago/cost-router@1.0.0", "kind": "data", "digest": "sha256:11bc90"}},
      "backend": {"ref": "bwrap"}
    }
  ],
  "objects": [
    {"name": "board", "handler": {"ref": "swarmidx:jmlago/task-board@1.0.0", "kind": "code", "digest": "sha256:7b11ce"}}
  ],
  "topology": [["researcher", "board"], ["coder", "board"]]
}`

const overlayJSON = `{
  "v": 1, "kind": "swarm.overlay", "swarm": "research", "apply": "incremental",
  "events": [
    {"seq": 1, "op": "add_agent", "payload": {
      "name": "reviewer",
      "body": {"ref": "swarmidx:jmlago/strict-reviewer@2.0.1", "kind": "data", "digest": "sha256:c50e22"},
      "model": {"ref": "openrouter:anthropic/claude-sonnet-4", "attested": true},
      "backend": {"ref": "bwrap"}
    }},
    {"seq": 2, "op": "add_topology_edges", "payload": {"edges": [["reviewer", "board"]]}},
    {"seq": 3, "op": "scale_agent_group", "payload": {"base_name": "coder", "target_count": 3, "on_inflight": "drain"}},
    {"seq": 4, "op": "bump_package", "payload": {"target": "researcher", "field": "body", "from": "sha256:aaaa", "to": "sha256:bbbb", "migration": "state_migrate"}},
    {"seq": 5, "op": "remove_agent", "payload": {"name": "reviewer", "on_inflight": "drain"}}
  ]
}`

func TestParseStateValid(t *testing.T) {
	s, err := ParseState([]byte(seedJSON))
	if err != nil {
		t.Fatalf("ParseState: %v", err)
	}
	if s.Name != "research" || s.Phase != "desired" {
		t.Fatalf("unexpected header: %+v", s)
	}
	if len(s.Agents) != 2 || len(s.Objects) != 1 || len(s.Topology) != 2 {
		t.Fatalf("unexpected sizes: %d agents, %d objects, %d edges", len(s.Agents), len(s.Objects), len(s.Topology))
	}
	if s.Agents[1].Model.Slot != "policy" {
		t.Fatalf("researcher model slot = %q, want policy", s.Agents[1].Model.Slot)
	}
}

func TestParseStateRejects(t *testing.T) {
	cases := map[string]string{
		"wrong kind":       `{"v":1,"kind":"nope","name":"x","phase":"desired","agents":[]}`,
		"bad version":      `{"v":2,"kind":"swarm.state","name":"x","phase":"desired","agents":[]}`,
		"bad phase":        `{"v":1,"kind":"swarm.state","name":"x","phase":"weird","agents":[]}`,
		"missing agents":   `{"v":1,"kind":"swarm.state","name":"x","phase":"desired"}`,
		"backend swarmidx": `{"v":1,"kind":"swarm.state","name":"x","phase":"desired","agents":[{"name":"a","body":{"ref":"swarmidx:x/y@1","kind":"data","digest":"sha256:ab"},"model":{"ref":"openrouter:m"},"backend":{"ref":"swarmidx:x/y@1","kind":"code","digest":"sha256:ab"}}]}`,
		"body wrong kind":  `{"v":1,"kind":"swarm.state","name":"x","phase":"desired","agents":[{"name":"a","body":{"ref":"swarmidx:x/y@1","kind":"code","digest":"sha256:ab"},"model":{"ref":"openrouter:m"},"backend":{"ref":"bwrap"}}]}`,
		"dup name": `{"v":1,"kind":"swarm.state","name":"x","phase":"desired","agents":[
			{"name":"a","body":{"ref":"swarmidx:x/y@1","kind":"data","digest":"sha256:ab"},"model":{"ref":"openrouter:m"},"backend":{"ref":"bwrap"}},
			{"name":"a","body":{"ref":"swarmidx:x/y@1","kind":"data","digest":"sha256:ab"},"model":{"ref":"openrouter:m"},"backend":{"ref":"bwrap"}}]}`,
		"unknown edge": `{"v":1,"kind":"swarm.state","name":"x","phase":"desired","agents":[{"name":"a","body":{"ref":"swarmidx:x/y@1","kind":"data","digest":"sha256:ab"},"model":{"ref":"openrouter:m"},"backend":{"ref":"bwrap"}}],"topology":[["a","ghost"]]}`,
	}
	for name, doc := range cases {
		if _, err := ParseState([]byte(doc)); err == nil {
			t.Errorf("%s: expected error, got none", name)
		}
	}
}

func TestParseOverlayRejects(t *testing.T) {
	cases := map[string]string{
		"unknown op":       `{"v":1,"kind":"swarm.overlay","swarm":"s","events":[{"seq":1,"op":"nuke","payload":{}}]}`,
		"non-monotonic":    `{"v":1,"kind":"swarm.overlay","swarm":"s","events":[{"seq":2,"op":"set_options","payload":{"options":{}}},{"seq":1,"op":"set_options","payload":{"options":{}}}]}`,
		"bad bump digest":  `{"v":1,"kind":"swarm.overlay","swarm":"s","events":[{"seq":1,"op":"bump_package","payload":{"target":"a","field":"body","from":"nope","to":"sha256:ab"}}]}`,
		"bad policy value": `{"v":1,"kind":"swarm.overlay","swarm":"s","events":[{"seq":1,"op":"remove_agent","payload":{"name":"a","on_inflight":"explode"}}]}`,
	}
	for name, doc := range cases {
		if _, err := ParseOverlay([]byte(doc)); err == nil {
			t.Errorf("%s: expected error, got none", name)
		}
	}
}

func TestFoldEndToEnd(t *testing.T) {
	state, err := ParseState([]byte(seedJSON))
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	ov, err := ParseOverlay([]byte(overlayJSON))
	if err != nil {
		t.Fatalf("overlay: %v", err)
	}
	got, err := Fold(state, ov.Events)
	if err != nil {
		t.Fatalf("fold: %v", err)
	}

	if len(got.Agents) != 4 {
		t.Fatalf("want 4 agents, got %d: %v", len(got.Agents), agentNames(got))
	}
	for _, want := range []string{"researcher", "coder#1", "coder#2", "coder#3"} {
		if !got.hasAgent(want) {
			t.Errorf("missing agent %q (have %v)", want, agentNames(got))
		}
	}
	if got.hasAgent("coder") {
		t.Errorf("template agent coder should have been replaced by instances")
	}
	if got.hasAgent("reviewer") {
		t.Errorf("reviewer should have been removed")
	}

	// bump_package swapped researcher's body digest.
	for _, a := range got.Agents {
		if a.Name == "researcher" && a.Body.Digest != "sha256:bbbb" {
			t.Errorf("researcher body digest = %q, want sha256:bbbb", a.Body.Digest)
		}
	}

	// topology fanned coder out and dropped reviewer's edge.
	if len(got.Topology) != 4 {
		t.Errorf("want 4 edges, got %d: %v", len(got.Topology), got.Topology)
	}
	if !hasEdge(got, "coder#2", "board") {
		t.Errorf("missing fanned edge coder#2->board")
	}
	if hasEdge(got, "reviewer", "board") {
		t.Errorf("reviewer edge should be gone")
	}
}

func TestMaterializeRoundTrips(t *testing.T) {
	state, _ := ParseState([]byte(seedJSON))
	ov, _ := ParseOverlay([]byte(overlayJSON))
	got, err := Fold(state, ov.Events)
	if err != nil {
		t.Fatalf("fold: %v", err)
	}
	out, err := got.MarshalJSON()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	reparsed, err := ParseState(out)
	if err != nil {
		t.Fatalf("re-parse materialized output: %v\n%s", err, out)
	}
	if len(reparsed.Agents) != len(got.Agents) {
		t.Errorf("round-trip changed agent count: %d -> %d", len(got.Agents), len(reparsed.Agents))
	}
}

func TestFoldBumpDigestMismatch(t *testing.T) {
	state, _ := ParseState([]byte(seedJSON))
	bad := `{"v":1,"kind":"swarm.overlay","swarm":"research","events":[
		{"seq":1,"op":"bump_package","payload":{"target":"researcher","field":"body","from":"sha256:dead","to":"sha256:beef"}}]}`
	ov, err := ParseOverlay([]byte(bad))
	if err != nil {
		t.Fatalf("overlay: %v", err)
	}
	if _, err := Fold(state, ov.Events); err == nil {
		t.Fatalf("expected bump digest mismatch, got none")
	}
}

func TestResolveSwarmidx(t *testing.T) {
	s, err := ParseState([]byte(`{"v":1,"kind":"swarm.state","name":"x","phase":"desired",
		"agents":[{"name":"a","body":{"ref":"swarmidx:o/b@1","kind":"data"},
		"model":{"policy":{"ref":"swarmidx:o/p@1","kind":"data"}},"backend":{"ref":"bwrap"}}],
		"objects":[{"name":"obj","handler":{"ref":"swarmidx:o/h@1","kind":"code"}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	calls := map[string]bool{}
	err = s.ResolveSwarmidx(func(ref string) (string, error) {
		calls[ref] = true
		return "sha256:dead", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if s.Agents[0].Body.Digest != "sha256:dead" {
		t.Error("body not resolved")
	}
	if s.Agents[0].Model.Ref.Digest != "sha256:dead" {
		t.Error("policy ref not resolved")
	}
	if s.Objects[0].Handler.Digest != "sha256:dead" {
		t.Error("handler not resolved")
	}
	if s.Agents[0].Backend.Digest != "" {
		t.Error("non-swarmidx backend must NOT be resolved")
	}
	for _, want := range []string{"swarmidx:o/b@1", "swarmidx:o/p@1", "swarmidx:o/h@1"} {
		if !calls[want] {
			t.Errorf("did not resolve %s", want)
		}
	}
}

func agentNames(s State) []string {
	var names []string
	for _, a := range s.Agents {
		names = append(names, a.Name)
	}
	return names
}

func hasEdge(s State, from, to string) bool {
	for _, e := range s.Topology {
		if e.From == from && e.To == to {
			return true
		}
	}
	return false
}
