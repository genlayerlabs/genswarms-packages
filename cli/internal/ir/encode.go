package ir

import "encoding/json"

// Canonical JSON serialization of a swarm.state, so a materialized state round-
// trips and can feed the genswarms runtime. encoding/json sorts map keys, so
// output is deterministic. Refs and the model slot emit their genswarms shapes.

func refToMap(r Ref) map[string]any {
	m := map[string]any{"ref": r.Ref}
	if r.Kind != "" {
		m["kind"] = r.Kind
	}
	if r.Digest != "" {
		m["digest"] = r.Digest
	}
	if r.Attested {
		m["attested"] = true
	}
	if r.Host != "" {
		m["host"] = r.Host
	}
	return m
}

func modelToMap(ms ModelSlot) map[string]any {
	if ms.Slot == "policy" {
		return map[string]any{"policy": refToMap(ms.Ref)}
	}
	return refToMap(ms.Ref)
}

func (a Agent) toMap() map[string]any {
	m := map[string]any{
		"name":    a.Name,
		"body":    refToMap(a.Body),
		"model":   modelToMap(a.Model),
		"backend": refToMap(a.Backend),
	}
	if len(a.Overrides) > 0 {
		m["overrides"] = a.Overrides
	}
	if len(a.Config) > 0 {
		m["config"] = a.Config
	}
	return m
}

func (o Object) toMap() map[string]any {
	m := map[string]any{"name": o.Name, "handler": refToMap(o.Handler)}
	if len(o.Config) > 0 {
		m["config"] = o.Config
	}
	return m
}

func (s State) toMap() map[string]any {
	agents := make([]any, len(s.Agents))
	for i, a := range s.Agents {
		agents[i] = a.toMap()
	}
	m := map[string]any{
		"v":      formatVersion,
		"kind":   "swarm.state",
		"name":   s.Name,
		"phase":  s.Phase,
		"agents": agents,
	}
	if len(s.Objects) > 0 {
		objects := make([]any, len(s.Objects))
		for i, o := range s.Objects {
			objects[i] = o.toMap()
		}
		m["objects"] = objects
	}
	if len(s.Topology) > 0 {
		topo := make([]any, len(s.Topology))
		for i, e := range s.Topology {
			topo[i] = []any{e.From, e.To}
		}
		m["topology"] = topo
	}
	if len(s.Options) > 0 {
		m["options"] = s.Options
	}
	return m
}

// MarshalJSON emits the canonical swarm.state shape.
func (s State) MarshalJSON() ([]byte, error) { return json.Marshal(s.toMap()) }
