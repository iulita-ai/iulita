package config

import (
	"testing"

	"github.com/google/uuid"
	"github.com/knadh/koanf/providers/confmap"
	"github.com/knadh/koanf/v2"
)

func TestNativeConnectionOwnerEnvironmentKeysPreserveLeafNames(t *testing.T) {
	owner := uuid.NewString()
	values := map[string]interface{}{}
	for env, key := range map[string]string{"IULITA_SKILLS_CRAFT_NATIVE_OWNER_ID": "skills.craft.native_owner_id", "IULITA_SKILLS_TODOIST_NATIVE_OWNER_ID": "skills.todoist.native_owner_id", "IULITA_SKILLS_SLACK_OAUTH_NATIVE_OWNER_ID": "skills.slack_oauth.native_owner_id"} {
		actual, ok := mobileEnvironmentKey(env)
		if !ok || actual != key {
			t.Fatal(env, actual)
		}
		values[actual] = owner
	}
	k := koanf.New(".")
	if err := k.Load(confmap.Provider(values, "."), nil); err != nil {
		t.Fatal(err)
	}
	var c Config
	if err := k.Unmarshal("", &c); err != nil {
		t.Fatal(err)
	}
	if c.Skills.Craft.NativeOwnerID != owner || c.Skills.Todoist.NativeOwnerID != owner || c.Skills.SlackOAuth.NativeOwnerID != owner {
		t.Fatal("owner assignment lost during configuration decoding")
	}
}
