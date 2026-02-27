package tools

import (
	"context"
	"encoding/json"
	"testing"
)

type stubTool struct {
	name string
	desc string
}

func (s *stubTool) Name() string                { return s.name }
func (s *stubTool) Description() string         { return s.desc }
func (s *stubTool) Parameters() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (s *stubTool) Execute(_ context.Context, _ json.RawMessage) (json.RawMessage, error) {
	return json.RawMessage(`"ok"`), nil
}

func TestRegistry_RegisterAndGet(t *testing.T) {
	t.Parallel()

	r := NewRegistry()
	r.Register(&stubTool{name: "my_tool", desc: "does stuff"})

	tool, ok := r.Get("my_tool")
	if !ok {
		t.Fatal("expected tool to be found")
	}
	if tool.Name() != "my_tool" {
		t.Errorf("Name() = %q, want %q", tool.Name(), "my_tool")
	}
}

func TestRegistry_GetMissing(t *testing.T) {
	t.Parallel()

	r := NewRegistry()
	_, ok := r.Get("nonexistent")
	if ok {
		t.Fatal("expected ok=false for missing tool")
	}
}

func TestRegistry_ToToolDefs(t *testing.T) {
	t.Parallel()

	r := NewRegistry()
	r.Register(&stubTool{name: "tool_a", desc: "desc a"})
	r.Register(&stubTool{name: "tool_b", desc: "desc b"})

	defs := r.ToToolDefs()
	if len(defs) != 2 {
		t.Fatalf("len(defs) = %d, want 2", len(defs))
	}

	found := make(map[string]ToolDef)
	for _, d := range defs {
		found[d.Name] = d
	}

	for _, name := range []string{"tool_a", "tool_b"} {
		d, ok := found[name]
		if !ok {
			t.Errorf("missing tool def for %q", name)
			continue
		}
		if len(d.InputSchema) == 0 {
			t.Errorf("tool %q has empty InputSchema", name)
		}
	}

	if found["tool_a"].Description != "desc a" {
		t.Errorf("tool_a description = %q, want %q", found["tool_a"].Description, "desc a")
	}
}

func TestRegistry_RegisterOverwrites(t *testing.T) {
	t.Parallel()

	r := NewRegistry()
	r.Register(&stubTool{name: "dup", desc: "first"})
	r.Register(&stubTool{name: "dup", desc: "second"})

	tool, ok := r.Get("dup")
	if !ok {
		t.Fatal("expected tool to be found")
	}
	if tool.Description() != "second" {
		t.Errorf("Description() = %q, want %q (should be overwritten)", tool.Description(), "second")
	}

	defs := r.ToToolDefs()
	if len(defs) != 1 {
		t.Errorf("len(defs) = %d, want 1 after overwrite", len(defs))
	}
}
