package ir

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestExecutionRefRoundTrip(t *testing.T) {
	for name, input := range map[string]map[string]any{
		"bwrap options": {"ref": "bwrap", "opts": map[string]any{"network": "isolated", "memory_mb": float64(256)}},
		"apple image":   {"ref": "apple_container", "image": "agent:code", "opts": map[string]any{"cpus": float64(2)}},
		"codex":         {"ref": "tmux", "client": "codex", "opts": map[string]any{"approval_policy": "never"}},
		"claude":        {"ref": "tmux", "client": "claude"},
		"opencode":      {"ref": "tmux", "client": "opencode"},
		"ssh":           {"ref": "ssh", "host": "worker.example", "opts": map[string]any{"port": float64(2222)}},
		"empty options": {"ref": "mock", "opts": map[string]any{}},
		"null options":  {"ref": "mock", "opts": nil},
	} {
		t.Run(name, func(t *testing.T) {
			ref, err := ParseRef(input)
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(refToMap(ref))
			if err != nil {
				t.Fatal(err)
			}
			var got map[string]any
			if err := json.Unmarshal(encoded, &got); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, input) {
				t.Fatalf("reference changed during serialization: got %#v, want %#v", got, input)
			}
		})
	}
}

func TestExecutionRefRejectsInvalidMetadata(t *testing.T) {
	for name, input := range map[string]map[string]any{
		"empty image":         {"ref": "bwrap", "image": ""},
		"numeric image":       {"ref": "bwrap", "image": 42},
		"list options":        {"ref": "bwrap", "opts": []any{}},
		"string options":      {"ref": "bwrap", "opts": "unsafe"},
		"missing tmux client": {"ref": "tmux"},
		"unknown tmux client": {"ref": "tmux", "client": "shell"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseRef(input); err == nil {
				t.Fatal("expected invalid execution metadata to fail")
			}
		})
	}
}
