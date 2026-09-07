package provider

import (
	"context"
	"reflect"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/xiehengjian/terraform-provider-multica/internal/client"
)

func TestRemoteSkillReferenceDetection(t *testing.T) {
	for _, value := range []string{
		"https://github.com/acme/skills/tree/main/review",
		"github.com/acme/review",
		"https://skills.sh/acme/review",
		"https://clawhub.ai/acme/review",
	} {
		if !isRemoteSkillReference(value) {
			t.Errorf("isRemoteSkillReference(%q) = false", value)
		}
	}
	if isRemoteSkillReference("local-review") {
		t.Error("local skill name was treated as a remote reference")
	}
}

func TestSkillRequestBodyIncludesContentConfigAndFiles(t *testing.T) {
	files := types.SetValueMust(skillFileObjectType(), []attr.Value{
		types.ObjectValueMust(skillFileObjectType().AttrTypes, map[string]attr.Value{
			"path":    types.StringValue("references/checklist.md"),
			"content": types.StringValue("check"),
		}),
	})
	plan := skillResourceModel{
		Name:        types.StringValue("review"),
		Description: types.StringValue("Review skill"),
		Content:     types.StringValue("# Review"),
		Files:       files,
	}
	body := skillRequestBody(nil, plan)
	wantFiles := []map[string]string{{"path": "references/checklist.md", "content": "check"}}
	if body["name"] != "review" || body["content"] != "# Review" || body["description"] != "Review skill" {
		t.Fatalf("body = %#v", body)
	}
	if !reflect.DeepEqual(body["files"], wantFiles) {
		t.Fatalf("files = %#v, want %#v", body["files"], wantFiles)
	}
}

func TestValidateSkillPlanRejectsReservedSkillFile(t *testing.T) {
	files := types.SetValueMust(skillFileObjectType(), []attr.Value{
		types.ObjectValueMust(skillFileObjectType().AttrTypes, map[string]attr.Value{
			"path":    types.StringValue("SKILL.md"),
			"content": types.StringValue("duplicate"),
		}),
	})
	err := validateSkillPlan(context.Background(), skillResourceModel{
		Name:    types.StringValue("review"),
		Content: types.StringValue("# Review"),
		Files:   files,
	})
	if err == nil {
		t.Fatal("validateSkillPlan() error = nil, want reserved file error")
	}
}

func TestPreserveSkillRefsKeepsRemoteURLInAgentState(t *testing.T) {
	remoteURL := "https://skills.sh/acme/review"
	got := preserveSkillRefs([]any{remoteURL}, []client.Skill{{
		ID:   "skill-1",
		Name: "review",
		Config: map[string]any{
			"origin": map[string]any{"source_url": remoteURL},
		},
	}})
	if !reflect.DeepEqual(got, []string{remoteURL}) {
		t.Fatalf("refs = %#v, want %#v", got, []string{remoteURL})
	}
}

func TestSkillStateNormalizesEmptyRemoteConfigWhenUnset(t *testing.T) {
	state := skillStateFromAPI(skillResourceModel{Config: types.DynamicNull()}, client.Skill{
		ID:     "skill-1",
		Name:   "review",
		Config: map[string]any{},
	})
	if !state.Config.IsNull() {
		t.Fatalf("empty remote config = %#v, want null", state.Config)
	}

	explicit := skillStateFromAPI(skillResourceModel{Config: goToDynamic(map[string]any{})}, client.Skill{
		ID:     "skill-1",
		Name:   "review",
		Config: map[string]any{},
	})
	if explicit.Config.IsNull() {
		t.Fatal("explicit empty config should remain configured")
	}
}
