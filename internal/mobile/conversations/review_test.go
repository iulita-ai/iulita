package conversations

import (
	"testing"
)

func TestContextRowLimitKeepsWholePredecessors(t *testing.T) {
	r, _, owner, _, _ := fixture(t)
	c := create(t, r, owner)
	rt, err := r.AcquireRuntime(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 51; i++ {
		if _, _, err = r.Accept(ctx, owner, c.ID, input("user")); err != nil {
			t.Fatal(err)
		}
		_, f, e := r.Claim(ctx, rt, owner)
		if e != nil {
			t.Fatal(e)
		}
		text := "answer"
		if i == 50 {
			text = ""
		}
		if e = r.Complete(ctx, f, text, "completed"); e != nil {
			t.Fatal(e)
		}
	}
	_, _, _ = r.Accept(ctx, owner, c.ID, input("next"))
	_, f, err := r.Claim(ctx, rt, owner)
	if err != nil {
		t.Fatal(err)
	}
	history, _, err := r.ContextFor(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) > 0 && history[0].Role != "user" {
		t.Fatalf("history starts with orphan %s after SQL row limit", history[0].Role)
	}
}
