package provider

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestReadPluginBundleIsDeterministic(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"multica.plugin.json": `{"key":"com.example.relay","version":"1.2.3","scopes":["issues:read"]}`,
		"hooks/bark.js":       "export default async function hook() {}\n",
		".git/ignored":        "must not be packaged",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(name)), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	first, err := readPluginBundle(root)
	if err != nil {
		t.Fatalf("first readPluginBundle() error = %v", err)
	}
	second, err := readPluginBundle(root)
	if err != nil {
		t.Fatalf("second readPluginBundle() error = %v", err)
	}
	if first.contentHash == "" || first.contentHash != second.contentHash {
		t.Fatalf("content hashes = %q and %q", first.contentHash, second.contentHash)
	}
	if string(first.archive) != string(second.archive) {
		t.Fatal("deterministic package archives differ")
	}
	if first.manifest.Key != "com.example.relay" || first.manifest.Version != "1.2.3" {
		t.Fatalf("manifest = %+v", first.manifest)
	}
	override, err := readPluginBundleWithManifest(root, `{"key":"com.example.relay","version":"1.2.4","scopes":["issues:read"]}`)
	if err != nil {
		t.Fatalf("manifest override error = %v", err)
	}
	if override.manifest.Version != "1.2.4" {
		t.Fatalf("manifest override = %+v", override.manifest)
	}
}

func TestPlannedPluginScopesMustMatchManifest(t *testing.T) {
	manifest := pluginManifest{Scopes: []string{"comments:read", "issues:read"}}
	plan := pluginResourceModel{GrantedScopes: types.SetValueMust(types.StringType, stringValues([]string{"issues:read", "comments:read"}))}
	got, err := plannedPluginScopes(context.Background(), plan, manifest)
	if err != nil {
		t.Fatalf("plannedPluginScopes() error = %v", err)
	}
	if got[0] != "comments:read" || got[1] != "issues:read" {
		t.Fatalf("scopes = %#v, want sorted exact set", got)
	}

	plan.GrantedScopes = types.SetValueMust(types.StringType, stringValues([]string{"issues:read"}))
	if _, err := plannedPluginScopes(context.Background(), plan, manifest); err == nil {
		t.Fatal("plannedPluginScopes() error = nil for incomplete scope set")
	}
}
