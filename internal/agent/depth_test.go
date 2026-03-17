package agent

import (
	"context"
	"testing"
)

func TestDepthFromDefault(t *testing.T) {
	ctx := context.Background()
	if got := DepthFrom(ctx); got != 0 {
		t.Errorf("DepthFrom(empty ctx) = %d, want 0", got)
	}
}

func TestWithDepthAndDepthFrom(t *testing.T) {
	ctx := WithDepth(context.Background(), 1)
	if got := DepthFrom(ctx); got != 1 {
		t.Errorf("DepthFrom = %d, want 1", got)
	}

	// Nested depth.
	ctx2 := WithDepth(ctx, 2)
	if got := DepthFrom(ctx2); got != 2 {
		t.Errorf("DepthFrom (nested) = %d, want 2", got)
	}

	// Original context unchanged.
	if got := DepthFrom(ctx); got != 1 {
		t.Errorf("DepthFrom (original) = %d, want 1", got)
	}
}

func TestMaxDepthConstant(t *testing.T) {
	if MaxDepth != 1 {
		t.Errorf("MaxDepth = %d, want 1", MaxDepth)
	}
}
