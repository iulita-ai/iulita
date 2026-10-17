package skill

import "testing"

type nativeFixture struct{ mockSkill }

func (*nativeFixture) NativePolicy() NativePolicy {
	return NativePolicy{Revision: "1", Account: true, Actions: map[string]ActionPolicy{"list": {Effect: "read", Scopes: []string{"assistant:services:read"}}}}
}

func TestCatalogSharesDefinitionsProtectsBuiltinsAndCopiesPolicy(t *testing.T) {
	r := NewRegistry()
	s := &nativeFixture{mockSkill{name: "account"}}
	r.RegisterInGroup(s, "fixture")
	r.AddManifest(&Manifest{Name: "fixture", SystemPrompt: "fixture instructions"})
	entries := r.Catalog()
	if len(entries) != 1 || entries[0].Native == nil || entries[0].Instructions != "fixture instructions" || entries[0].Binding == "" {
		t.Fatal("missing policy")
	}
	first := entries[0].Binding
	entries[0].Native.Actions["list"].Scopes[0] = "forged"
	entries[0].Native.Actions["write"] = ActionPolicy{Effect: "mutating"}
	again := r.Catalog()
	if again[0].Native.Actions["list"].Scopes[0] != "assistant:services:read" || len(again[0].Native.Actions) != 1 {
		t.Fatal("mutable registry policy escaped")
	}
	r.RegisterExternalWithManifest(&nativeFixture{mockSkill{name: "account"}}, nil)
	got, _ := r.Get("account")
	if got != s {
		t.Fatal("external overwrote grouped builtin")
	}
	r.DisableSkill("account")
	if len(r.Catalog()) != 0 {
		t.Fatal("disabled catalog binding")
	}
	r.EnableSkill("account")
	r.Register(s)
	if r.Catalog()[0].Binding == first {
		t.Fatal("replacement retained old binding")
	}
	r.RegisterExternalWithManifest(&nativeFixture{mockSkill{name: "extension"}}, nil)
	for _, entry := range r.Catalog() {
		if entry.Definition.Name == "extension" && entry.Native != nil {
			t.Fatal("extension self-authorized native execution")
		}
	}
}
