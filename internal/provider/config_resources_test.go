package provider

import (
	"reflect"
	"testing"
)

func TestTriggerBodyAcceptsDeclarativeCronAlias(t *testing.T) {
	body, err := triggerBody(map[string]any{
		"kind":     "schedule",
		"cron":     "35 11 * * 1-5",
		"timezone": "Asia/Shanghai",
		"label":    "morning",
		"enabled":  true,
	})
	if err != nil {
		t.Fatalf("triggerBody() error = %v", err)
	}
	want := map[string]any{
		"kind":            "schedule",
		"cron_expression": "35 11 * * 1-5",
		"timezone":        "Asia/Shanghai",
		"label":           "morning",
		"enabled":         true,
	}
	if !reflect.DeepEqual(body, want) {
		t.Fatalf("body = %#v, want %#v", body, want)
	}
}

func TestCanonicalSquadConfigUsesDeclarativeReferences(t *testing.T) {
	got := canonicalSquadConfig(map[string]any{
		"name":        "research-team",
		"description": "validate research",
		"leader_id":   "agent-1",
		"members": []map[string]any{
			{"member_type": "agent", "member_id": "agent-1", "role": "leader"},
			{"member_type": "member", "member_id": "member-1", "role": "reviewer"},
		},
	})
	want := map[string]any{
		"name":    "research-team",
		"purpose": "validate research",
		"leader":  "agent-1",
		"members": []any{
			map[string]any{"agent": "agent-1", "role": "leader"},
			map[string]any{"member": "member-1", "role": "reviewer"},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("config = %#v, want %#v", got, want)
	}
}

func TestCanonicalAutopilotConfigUsesDeclarativeAliases(t *testing.T) {
	got := canonicalAutopilotConfig(map[string]any{
		"autopilot": map[string]any{
			"title":          "daily",
			"assignee_id":    "agent-1",
			"assignee_type":  "agent",
			"execution_mode": "create_issue",
			"project_id":     "project-1",
		},
		"triggers": []any{map[string]any{"kind": "schedule", "label": "morning"}},
	})
	if got["agent"] != "agent-1" || got["mode"] != "create_issue" || got["project"] != "project-1" {
		t.Fatalf("config = %#v", got)
	}
	if _, ok := got["assignee_id"]; ok {
		t.Fatalf("canonical config should use agent alias: %#v", got)
	}
}

func TestTriggerKeyPrefersStableLabel(t *testing.T) {
	if got := triggerKey(map[string]any{"kind": "schedule", "cron": "0 9 * * *", "label": "market-open"}); got != "label:market-open" {
		t.Fatalf("triggerKey() = %q", got)
	}
}
