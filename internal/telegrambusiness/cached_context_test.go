package telegrambusiness

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
)

func cachedFixture(t *testing.T) *fixture {
	t.Helper()
	f := setup(t, false)
	f.service.options.Budget = &testBudget{}
	if e := f.repo.Update(context.Background(), f.scope.Key, func(st *State) error {
		st.Connection.Policy.MediaConsent = true
		st.Connection.Policy.MediaProvider = "media"
		p := st.Peers[99]
		p.Policy.MediaConsent = true
		p.Policy.MediaProvider = "media"
		p.Messages[0].Text = "Attached source"
		p.Messages[0].ExtractedText = strings.Repeat("cached PDF evidence ", 1400)
		p.Messages[0].ExtractedProvider = "media"
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	return f
}
func TestLargeCachedSourceTriggersAutomaticAndManualCompaction(t *testing.T) {
	for _, manual := range []bool{false, true} {
		t.Run(map[bool]string{false: "automatic", true: "manual"}[manual], func(t *testing.T) {
			f := cachedFixture(t)
			var compact, draft atomic.Int32
			f.service.generator = generatorFunc(func(ctx context.Context, _ Scope, messages []Message) (string, error) {
				settings, _ := GenerationSettingsFrom(ctx)
				if contextBytes(messages) > 16000 {
					t.Error("cached context exceeded finite admission")
				}
				if settings.Operation == "compact" {
					compact.Add(1)
					for mIndex := range messages {
						m := messages[mIndex]
						if m.ExtractedText != "" || len(m.Media) > 0 {
							t.Error("compaction duplicated cache or re-extracted media")
						}
					}
					return "Faithful cached PDF summary", nil
				}
				draft.Add(1)
				return "Reply from cached evidence", nil
			})
			if manual {
				result, e := f.service.Compact(context.Background(), f.scope)
				if e != nil || !result.Changed || result.BeforeBytes <= 16000 || result.AfterBytes >= result.BeforeBytes {
					t.Fatal(result, e)
				}
			} else {
				if _, e := f.service.Draft(context.Background(), f.scope); e != nil {
					t.Fatal(e)
				}
			}
			if compact.Load() < 2 || !manual && draft.Load() != 1 {
				t.Fatal("large cached context did not compact", compact.Load(), draft.Load())
			}
			st, _ := f.repo.Get(context.Background(), f.scope.Key)
			if len(st.Peers[99].Messages[0].ExtractedText) <= 16000 {
				t.Fatal("compaction destroyed raw derived source")
			}
		})
	}
}
func TestMediaPolicyRevocationErasesCachedSummaryAndFiltersProviderMismatch(t *testing.T) {
	f := cachedFixture(t)
	f.service.generator = generatorFunc(func(context.Context, Scope, []Message) (string, error) { return "Derived source summary", nil })
	if _, e := f.service.Compact(context.Background(), f.scope); e != nil {
		t.Fatal(e)
	}
	st, _ := f.repo.Get(context.Background(), f.scope.Key)
	policy := st.Peers[99].Policy
	policy.MediaConsent = false
	if checkErr := f.service.Configure(context.Background(), ConfigureInput{Scope: f.scope, ExpectedRevision: st.Peers[99].Revision, Policy: policy}); checkErr != nil {
		t.Fatal(checkErr)
	}
	st, _ = f.repo.Get(context.Background(), f.scope.Key)
	p := st.Peers[99]
	if p.Summary != nil || p.Messages[0].ExtractedText != "" || p.Messages[0].ExtractedProvider != "" {
		t.Fatal("media-derived content survived revocation")
	}
	f.service.generator = generatorFunc(func(_ context.Context, _ Scope, messages []Message) (string, error) {
		for mIndex := range messages {
			m := messages[mIndex]
			if m.ExtractedText != "" || m.Direction == "summary" {
				t.Error("revoked cached context reached model")
			}
		}
		return "Ordinary text only", nil
	})
	if _, e := f.service.Draft(context.Background(), f.scope); e != nil {
		t.Fatal(e)
	}
}
func TestDeletedAndExpiredSourcesEraseAllDerivedMedia(t *testing.T) {
	f := cachedFixture(t)
	if checkErr := f.service.Purge(context.Background(), f.scope); checkErr != nil {
		t.Fatal(checkErr)
	}
	st, _ := f.repo.Get(context.Background(), f.scope.Key)
	m := st.Peers[99].Messages[0]
	if m.ExtractedText != "" || m.ExtractedProvider != "" || m.MediaGroupID != "" {
		t.Fatal("purge retained derived media")
	}
}

func TestCachedProviderMismatchNeverReachesModel(t *testing.T) {
	f := cachedFixture(t)
	if checkErr := f.repo.Update(context.Background(), f.scope.Key, func(st *State) error { st.Peers[99].Messages[0].ExtractedProvider = "different provider"; return nil }); checkErr != nil {
		t.Fatal(checkErr)
	}
	f.service.generator = generatorFunc(func(_ context.Context, _ Scope, messages []Message) (string, error) {
		for mIndex := range messages {
			m := messages[mIndex]
			if m.ExtractedText != "" || strings.Contains(m.Text, "cached PDF evidence") {
				t.Error("unconsented cached provider reached model")
			}
		}
		return "Text-only draft", nil
	})
	if _, e := f.service.Draft(context.Background(), f.scope); e != nil {
		t.Fatal(e)
	}
}
