package claude

import (
	"testing"

	"github.com/iulita-ai/iulita/internal/llm"
)

func TestBuildSystemBlocks_BothParts(t *testing.T) {
	req := llm.Request{
		StaticSystemPrompt: "You are a helpful assistant.",
		SystemPrompt:       "Current time: 2026-03-12",
	}
	blocks := buildSystemBlocks(req)
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(blocks))
	}

	// First block: static with cache control.
	if blocks[0].Text != "You are a helpful assistant." {
		t.Errorf("block[0] text = %q", blocks[0].Text)
	}
	if blocks[0].CacheControl.Type != "ephemeral" {
		t.Errorf("block[0] should have ephemeral cache control, got %q", blocks[0].CacheControl.Type)
	}

	// Second block: dynamic without cache control.
	if blocks[1].Text != "Current time: 2026-03-12" {
		t.Errorf("block[1] text = %q", blocks[1].Text)
	}
	if blocks[1].CacheControl.Type != "" {
		t.Errorf("block[1] should not have cache control, got %q", blocks[1].CacheControl.Type)
	}
}

func TestBuildSystemBlocks_StaticOnly(t *testing.T) {
	req := llm.Request{
		StaticSystemPrompt: "You are a helpful assistant.",
	}
	blocks := buildSystemBlocks(req)
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(blocks))
	}
	if blocks[0].CacheControl.Type != "ephemeral" {
		t.Errorf("static-only block should have cache control")
	}
}

func TestBuildSystemBlocks_DynamicOnly(t *testing.T) {
	req := llm.Request{
		SystemPrompt: "Current time: 2026-03-12",
	}
	blocks := buildSystemBlocks(req)
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(blocks))
	}
	if blocks[0].Text != "Current time: 2026-03-12" {
		t.Errorf("text = %q", blocks[0].Text)
	}
	if blocks[0].CacheControl.Type != "" {
		t.Errorf("dynamic-only block should not have cache control")
	}
}

func TestBuildSystemBlocks_Empty(t *testing.T) {
	req := llm.Request{}
	blocks := buildSystemBlocks(req)
	if blocks != nil {
		t.Fatalf("expected nil, got %d blocks", len(blocks))
	}
}
