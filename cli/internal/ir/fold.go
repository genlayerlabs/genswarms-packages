package ir

import (
	"fmt"
	"strconv"
)

// Fold applies an overlay's events to a swarm.state in seq order and returns the
// resulting state (§5.4). Pure and deterministic: no runtime effects. Each op
// enforces its precondition; folding stops at the first violation, attributing
// the failure to the offending event's seq. Go mirror of Genswarms.IR.Fold.
func Fold(state State, events []Event) (State, error) {
	s := state
	for _, e := range events {
		next, err := applyEvent(s, e)
		if err != nil {
			return State{}, fmt.Errorf("seq %d: %w", e.Seq, err)
		}
		s = next
	}
	return s, nil
}

func applyEvent(s State, e Event) (State, error) {
	switch e.Op {
	case "add_agent":
		agent, err := ParseAgent(e.Payload)
		if err != nil {
			return s, err
		}
		if s.nodeExists(agent.Name) {
			return s, fmt.Errorf("agent exists: %q", agent.Name)
		}
		s.Agents = append(append([]Agent{}, s.Agents...), agent)
		return s, nil

	case "add_object":
		object, err := ParseObject(e.Payload)
		if err != nil {
			return s, err
		}
		if s.nodeExists(object.Name) {
			return s, fmt.Errorf("object exists: %q", object.Name)
		}
		s.Objects = append(append([]Object{}, s.Objects...), object)
		return s, nil

	case "remove_agent":
		name, _ := e.Payload["name"].(string)
		if !s.hasAgent(name) {
			return s, fmt.Errorf("agent not found: %q", name)
		}
		s.Agents = rejectAgent(s.Agents, name)
		s.Topology = dropIncident(s.Topology, name)
		return s, nil

	case "remove_object":
		name, _ := e.Payload["name"].(string)
		if !s.hasObject(name) {
			return s, fmt.Errorf("object not found: %q", name)
		}
		s.Objects = rejectObject(s.Objects, name)
		s.Topology = dropIncident(s.Topology, name)
		return s, nil

	case "add_topology_edges":
		return s.addEdges(payloadEdges(e.Payload))

	case "remove_topology_edges":
		drop := edgeSet(payloadEdges(e.Payload))
		var kept []Edge
		for _, ed := range s.Topology {
			if !drop[ed] {
				kept = append(kept, ed)
			}
		}
		s.Topology = kept
		return s, nil

	case "set_options":
		opts, _ := asMap(e.Payload["options"])
		merged := map[string]any{}
		for k, v := range s.Options {
			merged[k] = v
		}
		for k, v := range opts {
			merged[k] = v
		}
		s.Options = merged
		return s, nil

	case "update_config":
		target, _ := e.Payload["target"].(string)
		cfg, _ := asMap(e.Payload["config"])
		return s.updateConfig(target, cfg)

	case "bump_package":
		return s.bump(e.Payload)

	case "scale_agent_group":
		base, _ := e.Payload["base_name"].(string)
		n, _ := asInt(e.Payload["target_count"])
		return s.scaleGroup(base, n)
	}
	return s, fmt.Errorf("unknown op: %q", e.Op)
}

// ── node helpers ─────────────────────────────────────────────────────────────

func (s State) nodeExists(name string) bool { return s.hasAgent(name) || s.hasObject(name) }

func (s State) hasAgent(name string) bool {
	for _, a := range s.Agents {
		if a.Name == name {
			return true
		}
	}
	return false
}

func (s State) hasObject(name string) bool {
	for _, o := range s.Objects {
		if o.Name == name {
			return true
		}
	}
	return false
}

func rejectAgent(agents []Agent, name string) []Agent {
	var out []Agent
	for _, a := range agents {
		if a.Name != name {
			out = append(out, a)
		}
	}
	return out
}

func rejectObject(objects []Object, name string) []Object {
	var out []Object
	for _, o := range objects {
		if o.Name != name {
			out = append(out, o)
		}
	}
	return out
}

func dropIncident(topology []Edge, name string) []Edge {
	var out []Edge
	for _, e := range topology {
		if e.From != name && e.To != name {
			out = append(out, e)
		}
	}
	return out
}

func payloadEdges(p map[string]any) []Edge {
	list, _ := asSlice(p["edges"])
	var edges []Edge
	for _, e := range list {
		pair, _ := asSlice(e)
		if len(pair) == 2 {
			f, _ := pair[0].(string)
			t, _ := pair[1].(string)
			edges = append(edges, Edge{From: f, To: t})
		}
	}
	return edges
}

func edgeSet(edges []Edge) map[Edge]bool {
	set := map[Edge]bool{}
	for _, e := range edges {
		set[e] = true
	}
	return set
}

func (s State) addEdges(edges []Edge) (State, error) {
	for _, e := range edges {
		if !s.nodeExists(e.From) {
			return s, fmt.Errorf("unknown edge endpoint: %q", e.From)
		}
		if !s.nodeExists(e.To) {
			return s, fmt.Errorf("unknown edge endpoint: %q", e.To)
		}
	}
	existing := edgeSet(s.Topology)
	topo := append([]Edge{}, s.Topology...)
	for _, e := range edges {
		if !existing[e] {
			topo = append(topo, e)
			existing[e] = true
		}
	}
	s.Topology = topo
	return s, nil
}

func (s State) updateConfig(target string, cfg map[string]any) (State, error) {
	for i, a := range s.Agents {
		if a.Name == target {
			agents := append([]Agent{}, s.Agents...)
			agents[i].Config = mergeMap(a.Config, cfg)
			s.Agents = agents
			return s, nil
		}
	}
	for i, o := range s.Objects {
		if o.Name == target {
			objects := append([]Object{}, s.Objects...)
			objects[i].Config = mergeMap(o.Config, cfg)
			s.Objects = objects
			return s, nil
		}
	}
	return s, fmt.Errorf("target not found: %q", target)
}

func mergeMap(base, over map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range base {
		out[k] = v
	}
	for k, v := range over {
		out[k] = v
	}
	return out
}

// ── bump_package (§4.5) ──────────────────────────────────────────────────────

func (s State) bump(p map[string]any) (State, error) {
	target, _ := p["target"].(string)
	field, _ := p["field"].(string)
	from, _ := p["from"].(string)
	to, _ := p["to"].(string)

	for i, a := range s.Agents {
		if a.Name == target {
			updated, err := bumpAgent(a, field, from, to)
			if err != nil {
				return s, err
			}
			agents := append([]Agent{}, s.Agents...)
			agents[i] = updated
			s.Agents = agents
			return s, nil
		}
	}
	for i, o := range s.Objects {
		if o.Name == target {
			updated, err := bumpObject(o, field, from, to)
			if err != nil {
				return s, err
			}
			objects := append([]Object{}, s.Objects...)
			objects[i] = updated
			s.Objects = objects
			return s, nil
		}
	}
	return s, fmt.Errorf("target not found: %q", target)
}

func bumpAgent(a Agent, field, from, to string) (Agent, error) {
	switch field {
	case "body":
		r, err := swapDigest(a.Body, from, to)
		if err != nil {
			return a, err
		}
		a.Body = r
	case "backend":
		r, err := swapDigest(a.Backend, from, to)
		if err != nil {
			return a, err
		}
		a.Backend = r
	case "model":
		r, err := swapDigest(a.Model.Ref, from, to)
		if err != nil {
			return a, err
		}
		a.Model.Ref = r
	default:
		return a, fmt.Errorf("invalid bump field: %q", field)
	}
	return a, nil
}

func bumpObject(o Object, field, from, to string) (Object, error) {
	if field != "handler" {
		return o, fmt.Errorf("invalid bump field: %q", field)
	}
	r, err := swapDigest(o.Handler, from, to)
	if err != nil {
		return o, err
	}
	o.Handler = r
	return o, nil
}

// swapDigest swaps a ref's digest, asserting the current digest matches `from`.
func swapDigest(r Ref, from, to string) (Ref, error) {
	if r.Digest != from {
		return r, fmt.Errorf("bump digest mismatch: expected %q, got %q", from, r.Digest)
	}
	r.Digest = to
	return r, nil
}

// ── scale_agent_group (§4.4) ─────────────────────────────────────────────────

func (s State) scaleGroup(base string, n int) (State, error) {
	var members []Agent
	for _, a := range s.Agents {
		if groupMember(a.Name, base) {
			members = append(members, a)
		}
	}
	if len(members) == 0 {
		return s, fmt.Errorf("scale base not found: %q", base)
	}

	template := members[0]
	for _, m := range members {
		if m.Name == base {
			template = m
			break
		}
	}
	template.Name = base

	var others []Agent
	for _, a := range s.Agents {
		if !groupMember(a.Name, base) {
			others = append(others, a)
		}
	}

	memberNames := map[string]bool{}
	for _, m := range members {
		memberNames[m.Name] = true
	}

	instances := make([]Agent, 0, n)
	for i := 1; i <= n; i++ {
		inst := template
		inst.Name = instanceName(base, i)
		instances = append(instances, inst)
	}

	s.Agents = append(others, instances...)
	s.Topology = fanOutEdges(s.Topology, base, memberNames, n)
	return s, nil
}

func groupMember(name, base string) bool {
	if name == base {
		return true
	}
	prefix := base + "#"
	if len(name) <= len(prefix) || name[:len(prefix)] != prefix {
		return false
	}
	suffix := name[len(prefix):]
	if _, err := strconv.Atoi(suffix); err != nil {
		return false
	}
	return true
}

func instanceName(base string, i int) string { return base + "#" + strconv.Itoa(i) }

func fanOutEdges(topology []Edge, base string, memberNames map[string]bool, n int) []Edge {
	inGroup := func(name string) bool { return name == base || memberNames[name] }
	var out []Edge
	seen := map[Edge]bool{}
	for _, e := range topology {
		nf := e.From
		if inGroup(nf) {
			nf = base
		}
		nt := e.To
		if inGroup(nt) {
			nt = base
		}
		for _, ne := range expandEdge(nf, nt, base, n) {
			if !seen[ne] {
				out = append(out, ne)
				seen[ne] = true
			}
		}
	}
	return out
}

func expandEdge(f, t, base string, n int) []Edge {
	switch {
	case f == base && t == base:
		out := make([]Edge, 0, n)
		for i := 1; i <= n; i++ {
			name := instanceName(base, i)
			out = append(out, Edge{From: name, To: name})
		}
		return out
	case f == base:
		out := make([]Edge, 0, n)
		for i := 1; i <= n; i++ {
			out = append(out, Edge{From: instanceName(base, i), To: t})
		}
		return out
	case t == base:
		out := make([]Edge, 0, n)
		for i := 1; i <= n; i++ {
			out = append(out, Edge{From: f, To: instanceName(base, i)})
		}
		return out
	default:
		return []Edge{{From: f, To: t}}
	}
}
