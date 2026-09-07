package provider

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/xiehengjian/terraform-provider-multica/internal/client"
)

var _ resource.Resource = (*pluginResource)(nil)
var _ resource.ResourceWithConfigure = (*pluginResource)(nil)
var _ resource.ResourceWithModifyPlan = (*pluginResource)(nil)

func newPluginResource() resource.Resource { return &pluginResource{} }

type pluginResource struct {
	client *client.Client
}

type pluginResourceModel struct {
	ID               types.String  `tfsdk:"id"`
	PackageDir       types.String  `tfsdk:"package_dir"`
	Manifest         types.String  `tfsdk:"manifest"`
	Enabled          types.Bool    `tfsdk:"enabled"`
	GrantedScopes    types.Set     `tfsdk:"granted_scopes"`
	Config           types.Dynamic `tfsdk:"config"`
	PluginKey        types.String  `tfsdk:"plugin_key"`
	Version          types.String  `tfsdk:"version"`
	PackageVersionID types.String  `tfsdk:"package_version_id"`
	ContentHash      types.String  `tfsdk:"content_hash"`
}

type pluginManifest struct {
	Key     string   `json:"key"`
	Version string   `json:"version"`
	Scopes  []string `json:"scopes"`
}

type pluginBundle struct {
	archive     []byte
	contentHash string
	manifest    pluginManifest
}

type pluginBundleFile struct {
	name string
	data []byte
}

func (r *pluginResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_plugin"
}

func (r *pluginResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"package_dir": schema.StringAttribute{
				Optional:    true,
				Description: "Directory containing multica.plugin.json and its referenced plugin files.",
			},
			"manifest": schema.StringAttribute{
				Optional:    true,
				Description: "Optional JSON manifest override for environment-specific hook URLs.",
			},
			"enabled": schema.BoolAttribute{
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(true),
				Description: "Whether the installed plugin is enabled.",
			},
			"granted_scopes": schema.SetAttribute{
				Optional:    true,
				Computed:    true,
				ElementType: types.StringType,
				Description: "Exact scopes granted to the plugin. If omitted, the manifest scopes are granted.",
			},
			"config": schema.DynamicAttribute{
				Optional:    true,
				Description: "Plain-text plugin configuration values. Secret values are not managed by this resource.",
			},
			"plugin_key": schema.StringAttribute{
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
				Description: "Plugin key from the package manifest.",
			},
			"version": schema.StringAttribute{
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
				Description: "Installed immutable plugin package version.",
			},
			"package_version_id": schema.StringAttribute{
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
				Description: "Multica package version ID used by the installation.",
			},
			"content_hash": schema.StringAttribute{
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
				Description: "SHA-256 hash of the deterministic package archive.",
			},
		},
	}
}

func (r *pluginResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", "Provider configuration did not return a Multica client.")
		return
	}
	r.client = c
}

func (r *pluginResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return
	}
	var plan pluginResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() || plan.PackageDir.IsNull() || plan.PackageDir.IsUnknown() || plan.PackageDir.ValueString() == "" {
		return
	}

	bundle, err := readPluginBundleWithManifest(plan.PackageDir.ValueString(), stringValueOrEmpty(plan.Manifest))
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("package_dir"), "Invalid Multica plugin package", err.Error())
		return
	}
	plan.PluginKey = types.StringValue(bundle.manifest.Key)
	plan.Version = types.StringValue(bundle.manifest.Version)
	plan.ContentHash = types.StringValue(bundle.contentHash)
	if plan.GrantedScopes.IsNull() || plan.GrantedScopes.IsUnknown() {
		plan.GrantedScopes = stringSetValue(bundle.manifest.Scopes)
	} else if _, err := plannedPluginScopes(ctx, plan, bundle.manifest); err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("granted_scopes"), "Invalid Multica plugin scopes", err.Error())
		return
	}
	if req.State.Raw.IsNull() {
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("plugin_key"), plan.PluginKey)...)
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("version"), plan.Version)...)
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("content_hash"), plan.ContentHash)...)
	} else {
		var state pluginResourceModel
		resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
		if resp.Diagnostics.HasError() {
			return
		}
		if state.PluginKey.IsNull() || state.PluginKey.IsUnknown() || state.PluginKey.ValueString() != plan.PluginKey.ValueString() {
			resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("plugin_key"), plan.PluginKey)...)
		}
		if state.Version.IsNull() || state.Version.IsUnknown() || state.Version.ValueString() != plan.Version.ValueString() {
			resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("version"), plan.Version)...)
		}
		if state.ContentHash.IsNull() || state.ContentHash.IsUnknown() || state.ContentHash.ValueString() != plan.ContentHash.ValueString() {
			resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("content_hash"), plan.ContentHash)...)
		}
	}
	if !plan.GrantedScopes.IsNull() && !plan.GrantedScopes.IsUnknown() {
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("granted_scopes"), plan.GrantedScopes)...)
	}
}

func (r *pluginResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan pluginResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	bundle, err := r.bundleForPlan(plan)
	if err != nil {
		resp.Diagnostics.AddError("Invalid Multica plugin package", err.Error())
		return
	}
	scopes, err := plannedPluginScopes(ctx, plan, bundle.manifest)
	if err != nil {
		resp.Diagnostics.AddError("Invalid Multica plugin scopes", err.Error())
		return
	}

	packageSummary, err := r.client.PublishPluginPackage(ctx, bundle.archive)
	if err != nil {
		resp.Diagnostics.AddError("Failed to publish Multica plugin package", immutableVersionError(err, bundle.manifest.Version))
		return
	}
	versionID, err := packageVersionID(packageSummary, bundle.manifest.Version)
	if err != nil {
		resp.Diagnostics.AddError("Published Multica plugin package has no matching version", err.Error())
		return
	}
	installation, err := r.client.InstallPlugin(ctx, versionID, scopes)
	if err != nil {
		resp.Diagnostics.AddError("Failed to install Multica plugin", err.Error())
		return
	}
	installation, err = r.applyPluginSettings(ctx, installation, plan)
	if err != nil {
		resp.Diagnostics.AddError("Failed to configure Multica plugin", err.Error())
		return
	}

	state := pluginStateFromAPI(plan, installation, bundle.contentHash)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *pluginResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state pluginResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	installation, err := r.findInstallation(ctx, state.ID.ValueString())
	if err != nil {
		if errors.Is(err, errPluginInstallationNotFound) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to read Multica plugin", err.Error())
		return
	}
	state = pluginStateFromAPI(state, installation, state.ContentHash.ValueString())
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *pluginResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan pluginResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var state pluginResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	installation, err := r.findInstallation(ctx, state.ID.ValueString())
	if err != nil {
		if errors.Is(err, errPluginInstallationNotFound) {
			resp.Diagnostics.AddError("Cannot update missing Multica plugin", "The plugin installation no longer exists; remove it from state and apply again.")
			return
		}
		resp.Diagnostics.AddError("Failed to read Multica plugin before update", err.Error())
		return
	}

	contentHash := state.ContentHash.ValueString()
	if !plan.PackageDir.IsNull() && !plan.PackageDir.IsUnknown() && plan.PackageDir.ValueString() != "" {
		bundle, bundleErr := readPluginBundleWithManifest(plan.PackageDir.ValueString(), stringValueOrEmpty(plan.Manifest))
		if bundleErr != nil {
			resp.Diagnostics.AddError("Invalid Multica plugin package", bundleErr.Error())
			return
		}
		if state.PluginKey.ValueString() != "" && bundle.manifest.Key != state.PluginKey.ValueString() {
			resp.Diagnostics.AddError("Cannot change the plugin key in place", "Use a separate multica_plugin resource when package_dir points to a different plugin key.")
			return
		}
		if bundle.contentHash != contentHash || bundle.manifest.Version != installation.Version {
			scopes, scopeErr := plannedPluginScopes(ctx, plan, bundle.manifest)
			if scopeErr != nil {
				resp.Diagnostics.AddError("Invalid Multica plugin scopes", scopeErr.Error())
				return
			}
			packageSummary, publishErr := r.client.PublishPluginPackage(ctx, bundle.archive)
			if publishErr != nil {
				resp.Diagnostics.AddError("Failed to publish Multica plugin package", immutableVersionError(publishErr, bundle.manifest.Version))
				return
			}
			versionID, versionErr := packageVersionID(packageSummary, bundle.manifest.Version)
			if versionErr != nil {
				resp.Diagnostics.AddError("Published Multica plugin package has no matching version", versionErr.Error())
				return
			}
			installation, err = r.client.InstallPlugin(ctx, versionID, scopes)
			if err != nil {
				resp.Diagnostics.AddError("Failed to upgrade Multica plugin", err.Error())
				return
			}
			contentHash = bundle.contentHash
		}
	}

	installation, err = r.applyPluginSettings(ctx, installation, plan)
	if err != nil {
		resp.Diagnostics.AddError("Failed to configure Multica plugin", err.Error())
		return
	}
	state = pluginStateFromAPI(plan, installation, contentHash)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *pluginResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state pluginResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.UninstallPlugin(ctx, state.ID.ValueString()); err != nil {
		var httpErr *client.HTTPError
		if errors.As(err, &httpErr) && httpErr.StatusCode == 404 {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to uninstall Multica plugin", err.Error())
		return
	}
	resp.State.RemoveResource(ctx)
}

func (r *pluginResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

func (r *pluginResource) bundleForPlan(plan pluginResourceModel) (pluginBundle, error) {
	if plan.PackageDir.IsNull() || plan.PackageDir.IsUnknown() || plan.PackageDir.ValueString() == "" {
		return pluginBundle{}, fmt.Errorf("package_dir is required when creating or changing a plugin")
	}
	return readPluginBundleWithManifest(plan.PackageDir.ValueString(), stringValueOrEmpty(plan.Manifest))
}

func (r *pluginResource) applyPluginSettings(ctx context.Context, installation client.PluginInstallation, plan pluginResourceModel) (client.PluginInstallation, error) {
	if !plan.Config.IsNull() && !plan.Config.IsUnknown() {
		config, err := attrToGo(plan.Config)
		if err != nil {
			return installation, fmt.Errorf("config: %w", err)
		}
		values, ok := config.(map[string]any)
		if !ok {
			return installation, fmt.Errorf("config must be an object")
		}
		installation, err = r.client.ConfigurePlugin(ctx, installation.ID, values)
		if err != nil {
			return installation, err
		}
	}
	if !plan.Enabled.IsNull() && !plan.Enabled.IsUnknown() && installation.Enabled != plan.Enabled.ValueBool() {
		var err error
		installation, err = r.client.SetPluginEnabled(ctx, installation.ID, plan.Enabled.ValueBool())
		if err != nil {
			return installation, err
		}
	}
	return installation, nil
}

var errPluginInstallationNotFound = errors.New("plugin installation not found")

func (r *pluginResource) findInstallation(ctx context.Context, id string) (client.PluginInstallation, error) {
	installations, err := r.client.ListPluginInstallations(ctx)
	if err != nil {
		return client.PluginInstallation{}, err
	}
	for _, installation := range installations {
		if installation.ID == id {
			return installation, nil
		}
	}
	return client.PluginInstallation{}, errPluginInstallationNotFound
}

func pluginStateFromAPI(previous pluginResourceModel, installation client.PluginInstallation, contentHash string) pluginResourceModel {
	previous.ID = types.StringValue(installation.ID)
	previous.PluginKey = types.StringValue(installation.PluginKey)
	previous.Version = types.StringValue(installation.Version)
	previous.PackageVersionID = types.StringValue(installation.PackageVersionID)
	previous.Enabled = types.BoolValue(installation.Enabled)
	previous.GrantedScopes = stringSetValue(installation.GrantedScopes)
	previous.ContentHash = types.StringValue(contentHash)
	if installation.Config != nil {
		previous.Config = goToDynamic(installation.Config)
	}
	return previous
}

func plannedPluginScopes(ctx context.Context, plan pluginResourceModel, manifest pluginManifest) ([]string, error) {
	scopes := append([]string(nil), manifest.Scopes...)
	if !plan.GrantedScopes.IsNull() && !plan.GrantedScopes.IsUnknown() {
		var configured []string
		if diags := plan.GrantedScopes.ElementsAs(ctx, &configured, false); diags.HasError() {
			return nil, fmt.Errorf("granted_scopes: %v", diags)
		}
		scopes = configured
	}
	if !sameStringSet(scopes, manifest.Scopes) {
		return nil, fmt.Errorf("granted_scopes must exactly match the package manifest scopes (%s)", strings.Join(normalizeStrings(manifest.Scopes), ", "))
	}
	return normalizeStrings(scopes), nil
}

func packageVersionID(summary client.PluginPackage, version string) (string, error) {
	for _, candidate := range summary.Versions {
		if candidate.Version == version {
			return candidate.ID, nil
		}
	}
	return "", fmt.Errorf("package %q did not return published version %q", summary.PluginKey, version)
}

func immutableVersionError(err error, version string) string {
	var httpErr *client.HTTPError
	if errors.As(err, &httpErr) && httpErr.StatusCode == 409 {
		return fmt.Sprintf("package version %q is immutable and already exists; bump the version in multica.plugin.json before publishing changes: %s", version, err)
	}
	return err.Error()
}

func readPluginBundle(dir string) (pluginBundle, error) {
	return readPluginBundleWithManifest(dir, "")
}

func readPluginBundleWithManifest(dir, manifestOverride string) (pluginBundle, error) {
	root := filepath.Clean(dir)
	info, err := os.Stat(root)
	if err != nil {
		return pluginBundle{}, err
	}
	if !info.IsDir() {
		return pluginBundle{}, fmt.Errorf("package_dir %q is not a directory", dir)
	}

	files := make([]pluginBundleFile, 0)
	var manifestData []byte
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if entry.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("symlink %q is not allowed in plugin packages", path)
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("plugin package entry %q is not a regular file", path)
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(relative)
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if name == "multica.plugin.json" {
			if manifestOverride != "" {
				data = []byte(manifestOverride)
			}
			manifestData = append([]byte(nil), data...)
		}
		files = append(files, pluginBundleFile{name: name, data: data})
		return nil
	})
	if err != nil {
		return pluginBundle{}, err
	}
	if manifestData == nil {
		return pluginBundle{}, fmt.Errorf("package_dir %q does not contain multica.plugin.json", dir)
	}

	var manifest pluginManifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		return pluginBundle{}, fmt.Errorf("parse multica.plugin.json: %w", err)
	}
	if manifest.Key == "" || manifest.Version == "" {
		return pluginBundle{}, fmt.Errorf("multica.plugin.json must define non-empty key and version")
	}

	sort.Slice(files, func(i, j int) bool { return files[i].name < files[j].name })
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	for _, file := range files {
		header := &zip.FileHeader{Name: file.name, Method: zip.Deflate}
		header.SetMode(0o644)
		header.Modified = time.Date(1980, time.January, 1, 0, 0, 0, 0, time.UTC)
		entry, err := writer.CreateHeader(header)
		if err != nil {
			return pluginBundle{}, err
		}
		if _, err := entry.Write(file.data); err != nil {
			return pluginBundle{}, err
		}
	}
	if err := writer.Close(); err != nil {
		return pluginBundle{}, err
	}
	digest := sha256.Sum256(archive.Bytes())
	return pluginBundle{
		archive:     archive.Bytes(),
		contentHash: hex.EncodeToString(digest[:]),
		manifest:    manifest,
	}, nil
}

func sameStringSet(left, right []string) bool {
	left = normalizeStrings(left)
	right = normalizeStrings(right)
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func normalizeStrings(values []string) []string {
	result := append([]string(nil), values...)
	sort.Strings(result)
	return result
}
