package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/xiehengjian/terraform-provider-multica/internal/client"
)

func TestValidateHookPlanRequiresEvent(t *testing.T) {
	plan := hookResourceModel{
		Name:    types.StringValue("require-mention"),
		Command: types.StringValue("agent-hook-kit"),
		Events:  stringSetValue(nil),
	}
	if err := validateHookPlan(context.Background(), plan); err == nil || err.Error() != "events must contain at least one event" {
		t.Fatalf("validateHookPlan() error = %v", err)
	}
}

func TestHookRequestBodyDefaultsToCodex(t *testing.T) {
	plan := hookResourceModel{
		Name:    types.StringValue("require-mention"),
		Command: types.StringValue("agent-hook-kit"),
		Events:  stringSetValue([]string{"Stop", "UserPromptSubmit"}),
	}
	body := hookRequestBody(context.Background(), plan)
	if got, ok := body["providers"].([]string); !ok || len(got) != 1 || got[0] != "codex" {
		t.Fatalf("providers = %#v, want [codex]", body["providers"])
	}
	if got, ok := body["events"].([]string); !ok || len(got) != 2 {
		t.Fatalf("events = %#v", body["events"])
	}
}

func TestHookStateFromAPIPreservesDefinition(t *testing.T) {
	config := goToDynamic(map[string]any{"timeout": int64(5)})
	previous := hookResourceModel{Config: config}
	state := hookStateFromAPI(previous, client.Hook{
		ID: "hook-1", Name: "require-mention", Command: "agent-hook-kit",
		Providers: []string{"codex"}, Events: []string{"Stop"}, Matcher: "tool == 'multica'",
		Config: map[string]any{"timeout": int64(10)},
	})
	if state.ID.ValueString() != "hook-1" || state.Name.ValueString() != "require-mention" || state.Command.ValueString() != "agent-hook-kit" {
		t.Fatalf("state = %+v", state)
	}
	var events []string
	if diags := state.Events.ElementsAs(context.Background(), &events, false); diags.HasError() || len(events) != 1 || events[0] != "Stop" {
		t.Fatalf("events = %v, diagnostics = %v", events, diags)
	}
	if state.Config.IsNull() {
		t.Fatal("hook config should be populated from the API")
	}

	values, diags := types.SetValue(types.StringType, []attr.Value{types.StringValue("codex")})
	if diags.HasError() {
		t.Fatalf("types.SetValue() diagnostics = %v", diags)
	}
	if providers, err := hookStringSet(context.Background(), values, "providers"); err != nil || len(providers) != 1 || providers[0] != "codex" {
		t.Fatalf("hookStringSet() = %v, error = %v", providers, err)
	}
}
