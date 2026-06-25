package ir

import (
	"encoding/json"
	"fmt"
)

// Overlay is swarm.overlay (IR2): an ordered event log folded over a swarm.state
// (§4). Both a versioned history and the control-plane command stream. Go mirror
// of Genswarms.IR.Overlay.
type Overlay struct {
	Swarm  string
	Apply  string // "incremental" | "transactional"
	Events []Event
}

// Event is an overlay event envelope (§4.2); payload stays a raw map.
type Event struct {
	Seq     int
	Op      string
	Payload map[string]any
}

// Ops is the §4.3 op catalogue. A string op outside this set fails validation —
// unknown ops are never silently ignored.
var Ops = map[string]bool{
	"add_agent": true, "remove_agent": true, "add_object": true, "remove_object": true,
	"add_topology_edges": true, "remove_topology_edges": true, "scale_agent_group": true,
	"bump_package": true, "set_options": true, "update_config": true,
}

var (
	onInflightPolicies = map[string]bool{"drain": true, "kill": true, "quarantine": true}
	migrationPolicies  = map[string]bool{"state_migrate": true, "restart": true}
	bumpFields         = map[string]bool{"body": true, "model": true, "backend": true, "handler": true}
	applyModes         = map[string]bool{"incremental": true, "transactional": true}
)

// ParseOverlay parses + validates a swarm.overlay JSON document.
func ParseOverlay(data []byte) (Overlay, error) {
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return Overlay{}, fmt.Errorf("overlay: %w", err)
	}
	return ParseOverlayMap(m)
}

// ParseOverlayMap parses a JSON-decoded swarm.overlay map.
func ParseOverlayMap(m map[string]any) (Overlay, error) {
	if err := checkVersion(m); err != nil {
		return Overlay{}, err
	}
	if err := checkKind(m, "swarm.overlay"); err != nil {
		return Overlay{}, err
	}
	swarm, ok := asString(m, "swarm")
	if !ok {
		return Overlay{}, fmt.Errorf("missing swarm")
	}
	apply, err := parseApply(m)
	if err != nil {
		return Overlay{}, err
	}
	rawEvents, ok := asSlice(m["events"])
	if !ok {
		return Overlay{}, fmt.Errorf("missing events")
	}
	events := make([]Event, 0, len(rawEvents))
	prevSeq := -1
	hasPrev := false
	for _, raw := range rawEvents {
		em, ok := asMap(raw)
		if !ok {
			return Overlay{}, fmt.Errorf("invalid event")
		}
		ev, err := parseEvent(em, prevSeq, hasPrev)
		if err != nil {
			return Overlay{}, err
		}
		events = append(events, ev)
		prevSeq = ev.Seq
		hasPrev = true
	}
	return Overlay{Swarm: swarm, Apply: apply, Events: events}, nil
}

func parseEvent(m map[string]any, prevSeq int, hasPrev bool) (Event, error) {
	seq, ok := asInt(m["seq"])
	if !ok {
		return Event{}, fmt.Errorf("invalid seq: %v", m["seq"])
	}
	if hasPrev && seq <= prevSeq { // §5.1: strictly ascending
		return Event{}, fmt.Errorf("non-monotonic seq: %d after %d", seq, prevSeq)
	}
	op, _ := m["op"].(string)
	if !Ops[op] {
		return Event{}, fmt.Errorf("unknown op: %q", op)
	}
	payload, ok := asMap(m["payload"])
	if !ok {
		return Event{}, fmt.Errorf("invalid payload")
	}
	if err := validatePayload(op, payload); err != nil {
		return Event{}, err
	}
	return Event{Seq: seq, Op: op, Payload: payload}, nil
}

// validatePayload checks a payload is structurally valid for its op (§4.3).
func validatePayload(op string, p map[string]any) error {
	switch op {
	case "add_agent":
		_, err := ParseAgent(p)
		return err
	case "add_object":
		_, err := ParseObject(p)
		return err
	case "remove_agent", "remove_object":
		if _, ok := asString(p, "name"); !ok {
			return fmt.Errorf("%s: missing name", op)
		}
		return optionalEnum(p, "on_inflight", onInflightPolicies)
	case "add_topology_edges", "remove_topology_edges":
		return validateEdges(p)
	case "set_options":
		return requireMap(p, "options")
	case "update_config":
		if _, ok := asString(p, "target"); !ok {
			return fmt.Errorf("update_config: missing target")
		}
		return requireMap(p, "config")
	case "scale_agent_group":
		if _, ok := asString(p, "base_name"); !ok {
			return fmt.Errorf("scale_agent_group: missing base_name")
		}
		if n, ok := asInt(p["target_count"]); !ok || n < 0 {
			return fmt.Errorf("scale_agent_group: invalid target_count: %v", p["target_count"])
		}
		return optionalEnum(p, "on_inflight", onInflightPolicies)
	case "bump_package":
		if _, ok := asString(p, "target"); !ok {
			return fmt.Errorf("bump_package: missing target")
		}
		if f, _ := p["field"].(string); !bumpFields[f] {
			return fmt.Errorf("bump_package: invalid field: %v", p["field"])
		}
		if d, _ := p["from"].(string); !ValidDigest(d) {
			return fmt.Errorf("bump_package: invalid from digest: %v", p["from"])
		}
		if d, _ := p["to"].(string); !ValidDigest(d) {
			return fmt.Errorf("bump_package: invalid to digest: %v", p["to"])
		}
		if err := optionalEnum(p, "migration", migrationPolicies); err != nil {
			return err
		}
		return optionalEnum(p, "on_inflight", onInflightPolicies)
	}
	return fmt.Errorf("unknown op: %q", op)
}

func validateEdges(p map[string]any) error {
	list, ok := asSlice(p["edges"])
	if !ok {
		return fmt.Errorf("missing edges")
	}
	for _, e := range list {
		pair, ok := asSlice(e)
		if !ok || len(pair) != 2 {
			return fmt.Errorf("invalid edges")
		}
		if _, ok := pair[0].(string); !ok {
			return fmt.Errorf("invalid edges")
		}
		if _, ok := pair[1].(string); !ok {
			return fmt.Errorf("invalid edges")
		}
	}
	return nil
}

func requireMap(p map[string]any, key string) error {
	if _, ok := asMap(p[key]); !ok {
		return fmt.Errorf("missing field %q", key)
	}
	return nil
}

func optionalEnum(p map[string]any, key string, allowed map[string]bool) error {
	v, present := p[key]
	if !present || v == nil {
		return nil
	}
	s, ok := v.(string)
	if !ok || !allowed[s] {
		return fmt.Errorf("invalid policy %q: %v", key, v)
	}
	return nil
}

func parseApply(m map[string]any) (string, error) {
	v, present := m["apply"]
	if !present {
		return "incremental", nil
	}
	s, ok := v.(string)
	if !ok || !applyModes[s] {
		return "", fmt.Errorf("invalid apply mode: %v", v)
	}
	return s, nil
}
