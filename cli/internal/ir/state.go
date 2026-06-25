package ir

import (
	"encoding/json"
	"fmt"
)

// State is swarm.state (IR1): a snapshot of a swarm in one of two phases (§3).
// Go mirror of Genswarms.IR.State.
type State struct {
	Name     string
	Phase    string // "desired" | "observed"
	Agents   []Agent
	Objects  []Object
	Topology []Edge
	Options  map[string]any
}

// Agent is an agent node (§3.2): name + slot-typed refs.
type Agent struct {
	Name      string
	Body      Ref       // kind: data
	Model     ModelSlot // {service, ref} | {policy, ref⟨data⟩}
	Backend   Ref       // non-swarmidx execution ref
	Overrides map[string]any
	Config    map[string]any
}

// Object is a non-agentic object node (§3.4); its handler is kind: code.
type Object struct {
	Name    string
	Handler Ref
	Config  map[string]any
}

// ModelSlot is an agent's model: either a service ref or a {policy: ref} wrap.
type ModelSlot struct {
	Slot string // "service" | "policy"
	Ref  Ref
}

// Edge is a directed topology edge [from, to].
type Edge struct {
	From string
	To   string
}

const formatVersion = 1

// ParseState parses + validates a swarm.state JSON document.
func ParseState(data []byte) (State, error) {
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return State{}, fmt.Errorf("state: %w", err)
	}
	return ParseStateMap(m)
}

// ParseStateMap parses a JSON-decoded swarm.state map.
func ParseStateMap(m map[string]any) (State, error) {
	if err := checkVersion(m); err != nil {
		return State{}, err
	}
	if err := checkKind(m, "swarm.state"); err != nil {
		return State{}, err
	}
	name, ok := asString(m, "name")
	if !ok {
		return State{}, fmt.Errorf("missing name")
	}
	phase, err := parsePhase(m)
	if err != nil {
		return State{}, err
	}

	rawAgents, ok := asSlice(m["agents"])
	if !ok {
		return State{}, fmt.Errorf("missing agents")
	}
	agents := make([]Agent, 0, len(rawAgents))
	for _, a := range rawAgents {
		am, ok := asMap(a)
		if !ok {
			return State{}, fmt.Errorf("invalid agent")
		}
		ag, err := ParseAgent(am)
		if err != nil {
			return State{}, err
		}
		agents = append(agents, ag)
	}

	var objects []Object
	if rawObjects, ok := asSlice(m["objects"]); ok {
		for _, o := range rawObjects {
			om, ok := asMap(o)
			if !ok {
				return State{}, fmt.Errorf("invalid object")
			}
			ob, err := ParseObject(om)
			if err != nil {
				return State{}, err
			}
			objects = append(objects, ob)
		}
	}

	topology, err := parseTopology(m)
	if err != nil {
		return State{}, err
	}

	s := State{
		Name:     name,
		Phase:    phase,
		Agents:   agents,
		Objects:  objects,
		Topology: topology,
		Options:  asStringMap(m, "options"),
	}
	if err := s.Validate(); err != nil {
		return State{}, err
	}
	return s, nil
}

// Validate runs the data-level §6 invariants (unique names, valid edges).
func (s State) Validate() error {
	if err := s.uniqueNames(); err != nil {
		return err
	}
	return s.validEdges()
}

// ValidateResolved checks the resolved-form digest rule on every ref (§6.4).
func (s State) ValidateResolved() error {
	for _, r := range s.Refs() {
		if err := r.ValidateResolved(); err != nil {
			return err
		}
	}
	return nil
}

// Refs returns all refs in the state (bodies, model refs, backends, handlers).
func (s State) Refs() []Ref {
	var refs []Ref
	for _, a := range s.Agents {
		refs = append(refs, a.Body, a.Backend, a.Model.Ref)
	}
	for _, o := range s.Objects {
		refs = append(refs, o.Handler)
	}
	return refs
}

// ParseAgent parses+validates a single agent map (slot-typed). Reused by Overlay.
func ParseAgent(m map[string]any) (Agent, error) {
	name, ok := asString(m, "name")
	if !ok {
		return Agent{}, fmt.Errorf("agent: missing name")
	}
	body, err := parseTypedRef(m, "body", "data")
	if err != nil {
		return Agent{}, err
	}
	model, err := parseModelSlot(m)
	if err != nil {
		return Agent{}, err
	}
	backend, err := parseBackend(m)
	if err != nil {
		return Agent{}, err
	}
	return Agent{
		Name:      name,
		Body:      body,
		Model:     model,
		Backend:   backend,
		Overrides: asStringMap(m, "overrides"),
		Config:    asStringMap(m, "config"),
	}, nil
}

// ParseObject parses+validates a single object map (handler is kind:code).
func ParseObject(m map[string]any) (Object, error) {
	name, ok := asString(m, "name")
	if !ok {
		return Object{}, fmt.Errorf("object: missing name")
	}
	handler, err := parseTypedRef(m, "handler", "code")
	if err != nil {
		return Object{}, err
	}
	return Object{Name: name, Handler: handler, Config: asStringMap(m, "config")}, nil
}

func parseTypedRef(m map[string]any, key, expectedKind string) (Ref, error) {
	rm, ok := asMap(m[key])
	if !ok {
		return Ref{}, fmt.Errorf("missing slot %q", key)
	}
	r, err := ParseRef(rm)
	if err != nil {
		return Ref{}, err
	}
	if r.Kind != expectedKind {
		return Ref{}, fmt.Errorf("slot %q type mismatch: expected %s, got %q", key, expectedKind, r.Kind)
	}
	return r, nil
}

func parseBackend(m map[string]any) (Ref, error) {
	rm, ok := asMap(m["backend"])
	if !ok {
		return Ref{}, fmt.Errorf("missing slot %q", "backend")
	}
	r, err := ParseRef(rm)
	if err != nil {
		return Ref{}, err
	}
	if r.Scheme == "swarmidx" {
		return Ref{}, fmt.Errorf("slot \"backend\": swarmidx not allowed")
	}
	return r, nil
}

func parseModelSlot(m map[string]any) (ModelSlot, error) {
	mv, ok := asMap(m["model"])
	if !ok {
		if m["model"] == nil {
			return ModelSlot{}, fmt.Errorf("missing slot %q", "model")
		}
		return ModelSlot{}, fmt.Errorf("invalid model slot")
	}
	if pol, ok := asMap(mv["policy"]); ok {
		r, err := ParseRef(pol)
		if err != nil {
			return ModelSlot{}, err
		}
		if r.Kind != "data" {
			return ModelSlot{}, fmt.Errorf("slot \"model.policy\" type mismatch: expected data, got %q", r.Kind)
		}
		return ModelSlot{Slot: "policy", Ref: r}, nil
	}
	r, err := ParseRef(mv)
	if err != nil {
		return ModelSlot{}, err
	}
	if r.Scheme == "swarmidx" {
		return ModelSlot{}, fmt.Errorf("slot \"model\": service ref must not be swarmidx")
	}
	return ModelSlot{Slot: "service", Ref: r}, nil
}

func parseTopology(m map[string]any) ([]Edge, error) {
	raw, present := m["topology"]
	if !present {
		return nil, nil
	}
	list, ok := asSlice(raw)
	if !ok {
		return nil, fmt.Errorf("invalid topology")
	}
	edges := make([]Edge, 0, len(list))
	for _, e := range list {
		pair, ok := asSlice(e)
		if !ok || len(pair) != 2 {
			return nil, fmt.Errorf("invalid edge: %v", e)
		}
		from, fok := pair[0].(string)
		to, tok := pair[1].(string)
		if !fok || !tok {
			return nil, fmt.Errorf("invalid edge: %v", e)
		}
		edges = append(edges, Edge{From: from, To: to})
	}
	return edges, nil
}

func (s State) uniqueNames() error {
	seen := map[string]bool{}
	for _, a := range s.Agents {
		if seen[a.Name] {
			return fmt.Errorf("duplicate name: %q", a.Name)
		}
		seen[a.Name] = true
	}
	for _, o := range s.Objects {
		if seen[o.Name] {
			return fmt.Errorf("duplicate name: %q", o.Name)
		}
		seen[o.Name] = true
	}
	return nil
}

func (s State) nodeSet() map[string]bool {
	set := map[string]bool{}
	for _, a := range s.Agents {
		set[a.Name] = true
	}
	for _, o := range s.Objects {
		set[o.Name] = true
	}
	return set
}

func (s State) validEdges() error {
	nodes := s.nodeSet()
	for _, e := range s.Topology {
		if !nodes[e.From] {
			return fmt.Errorf("unknown edge endpoint: %q", e.From)
		}
		if !nodes[e.To] {
			return fmt.Errorf("unknown edge endpoint: %q", e.To)
		}
	}
	return nil
}

func checkVersion(m map[string]any) error {
	v, ok := asInt(m["v"])
	if !ok || v != formatVersion {
		return fmt.Errorf("unsupported version: %v", m["v"])
	}
	return nil
}

func checkKind(m map[string]any, want string) error {
	if k, _ := m["kind"].(string); k != want {
		return fmt.Errorf("wrong kind: %v (want %s)", m["kind"], want)
	}
	return nil
}

func parsePhase(m map[string]any) (string, error) {
	switch p, _ := m["phase"].(string); p {
	case "desired", "observed":
		return p, nil
	default:
		return "", fmt.Errorf("invalid phase: %v", m["phase"])
	}
}
