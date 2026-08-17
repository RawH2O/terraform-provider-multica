package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/xiehengjian/terraform-provider-multica/internal/client"
)

var _ resource.Resource = (*agentResource)(nil)
var _ resource.ResourceWithConfigure = (*agentResource)(nil)
var _ resource.ResourceWithModifyPlan = (*agentResource)(nil)

func newAgentResource() resource.Resource {
	return &agentResource{}
}

type agentResource struct {
	client *client.Client
}

type agentResourceModel struct {
	ID          types.String  `tfsdk:"id"`
	Config      types.Dynamic `tfsdk:"config"`
	ContentHash types.String  `tfsdk:"content_hash"`
}

type agentConfigModel struct {
	Name                     types.String         `tfsdk:"name"`
	Description              types.String         `tfsdk:"description"`
	Instructions             types.String         `tfsdk:"instructions"`
	AvatarURL                types.String         `tfsdk:"avatar_url"`
	Runtime                  runtimeSelectorModel `tfsdk:"runtime"`
	RuntimeConfig            types.String         `tfsdk:"runtime_config"`
	Model                    types.String         `tfsdk:"model"`
	ThinkingLevel            types.String         `tfsdk:"thinking_level"`
	MaxConcurrentTasks       types.Int64          `tfsdk:"max_concurrent_tasks"`
	CustomArgs               types.List           `tfsdk:"custom_args"`
	PermissionMode           types.String         `tfsdk:"permission_mode"`
	Visibility               types.String         `tfsdk:"visibility"`
	Skills                   types.Set            `tfsdk:"skills"`
	InvocationTargets        types.Set            `tfsdk:"invocation_targets"`
	CustomEnv                types.Map            `tfsdk:"custom_env"`
	MCPConfig                types.String         `tfsdk:"mcp_config"`
	ComposioToolkitAllowlist types.Set            `tfsdk:"composio_toolkit_allowlist"`
	Archived                 types.Bool           `tfsdk:"archived"`
	UnsupportedFields        []string
}

type runtimeSelectorModel struct {
	ID         types.String `tfsdk:"id"`
	Name       types.String `tfsdk:"name"`
	CustomName types.String `tfsdk:"custom_name"`
	Provider   types.String `tfsdk:"provider"`
	Reference  types.String `tfsdk:"reference"`
}

type invocationTargetModel struct {
	TargetType types.String `tfsdk:"target_type"`
	TargetID   types.String `tfsdk:"target_id"`
}

func (r *agentResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_agent"
}

func (r *agentResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed: true,
			},
			// Dynamic is intentional: multica-declarative has compact and
			// expanded union forms (model, skills, permission), plus arbitrary
			// runtime_config and file references. Terraform's yamldecode()
			// preserves those shapes for the provider to validate.
			"config": schema.DynamicAttribute{Required: true},
			"content_hash": schema.StringAttribute{
				Computed:    true,
				Description: "Hash of the declaration and referenced file contents.",
			},
		},
	}
}

func (r *agentResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *agentResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan agentResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	config, err := decodeDeclarativeConfig(ctx, plan.Config)
	if err != nil {
		resp.Diagnostics.AddError("Invalid agent config", err.Error())
		return
	}
	if err := validateAgentConfig(config); err != nil {
		resp.Diagnostics.AddError("Invalid agent config", err.Error())
		return
	}

	body, skillIDs, err := r.requestBody(ctx, config)
	if err != nil {
		resp.Diagnostics.AddError("Invalid agent config", err.Error())
		return
	}
	created, err := r.client.CreateAgent(ctx, body)
	if err != nil {
		resp.Diagnostics.AddError("Failed to create Multica agent", err.Error())
		return
	}
	if err := r.client.SetAgentSkills(ctx, created.ID, skillIDs); err != nil {
		r.rollbackCreatedAgent(ctx, created.ID)
		resp.Diagnostics.AddError("Failed to assign agent skills", err.Error())
		return
	}
	if !config.CustomEnv.IsNull() && !config.CustomEnv.IsUnknown() {
		env, err := stringMap(ctx, config.CustomEnv)
		if err != nil {
			r.rollbackCreatedAgent(ctx, created.ID)
			resp.Diagnostics.AddError("Invalid custom_env", err.Error())
			return
		}
		if err := r.client.SetAgentEnv(ctx, created.ID, env); err != nil {
			r.rollbackCreatedAgent(ctx, created.ID)
			resp.Diagnostics.AddError("Failed to set agent environment", err.Error())
			return
		}
	}
	if !config.Archived.IsNull() && !config.Archived.IsUnknown() && config.Archived.ValueBool() {
		if err := r.client.ArchiveAgent(ctx, created.ID); err != nil {
			r.rollbackCreatedAgent(ctx, created.ID)
			resp.Diagnostics.AddError("Failed to archive Multica agent", err.Error())
			return
		}
	}

	plan.ID = types.StringValue(created.ID)
	plan.ContentHash = types.StringValue(declarativeContentHashOrEmpty(plan.Config))
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *agentResource) rollbackCreatedAgent(ctx context.Context, id string) {
	// Terraform will retry a failed create. Best-effort archival prevents a
	// partially-created agent from becoming an unmanaged workspace object when
	// a follow-up skills/env/archive call fails.
	_ = r.client.ArchiveAgent(ctx, id)
}

func (r *agentResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	// Terraform supplies a null plan when a state-only resource is being
	// destroyed because its declaration was removed from configuration. There
	// is no content hash to compute in that case.
	if req.Plan.Raw.IsNull() {
		return
	}
	var plan agentResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() || plan.Config.IsNull() || plan.Config.IsUnknown() {
		return
	}
	hash, err := declarativeContentHash(plan.Config)
	if err != nil {
		resp.Diagnostics.AddError("Failed to hash agent declaration", err.Error())
		return
	}
	plan.ContentHash = types.StringValue(hash)
	resp.Diagnostics.Append(resp.Plan.Set(ctx, &plan)...)
}

func (r *agentResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state agentResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	agent, err := r.client.GetAgent(ctx, state.ID.ValueString())
	if err != nil {
		var httpErr *client.HTTPError
		if errors.As(err, &httpErr) && httpErr.StatusCode == 404 {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to read Multica agent", err.Error())
		return
	}
	if state.Config.IsNull() || state.Config.IsUnknown() {
		current := stateConfigFromAgent(agentConfigModel{}, agent)
		state.Config = encodeDeclarativeConfig(current)
	} else if merged, mergeErr := mergeDeclarativeState(ctx, state.Config, agent); mergeErr == nil {
		state.Config = merged
	} else {
		state.Config = encodeDeclarativeConfig(stateConfigFromAgent(agentConfigModel{}, agent))
	}
	if hash, hashErr := declarativeContentHash(state.Config); hashErr == nil {
		state.ContentHash = types.StringValue(hash)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *agentResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan agentResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	config, err := decodeDeclarativeConfig(ctx, plan.Config)
	if err != nil {
		resp.Diagnostics.AddError("Invalid agent config", err.Error())
		return
	}
	if err := validateAgentConfig(config); err != nil {
		resp.Diagnostics.AddError("Invalid agent config", err.Error())
		return
	}
	body, skillIDs, err := r.requestBody(ctx, config)
	if err != nil {
		resp.Diagnostics.AddError("Invalid agent config", err.Error())
		return
	}
	var state agentResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if _, err := r.client.UpdateAgent(ctx, state.ID.ValueString(), body); err != nil {
		resp.Diagnostics.AddError("Failed to update Multica agent", err.Error())
		return
	}

	if err := r.client.SetAgentSkills(ctx, state.ID.ValueString(), skillIDs); err != nil {
		resp.Diagnostics.AddError("Failed to update agent skills", err.Error())
		return
	}
	if !config.CustomEnv.IsNull() && !config.CustomEnv.IsUnknown() {
		env, err := stringMap(ctx, config.CustomEnv)
		if err != nil {
			resp.Diagnostics.AddError("Invalid custom_env", err.Error())
			return
		}
		if err := r.client.SetAgentEnv(ctx, state.ID.ValueString(), env); err != nil {
			resp.Diagnostics.AddError("Failed to update agent environment", err.Error())
			return
		}
	}
	if !config.Archived.IsNull() && !config.Archived.IsUnknown() {
		if config.Archived.ValueBool() {
			if err := r.client.ArchiveAgent(ctx, state.ID.ValueString()); err != nil {
				resp.Diagnostics.AddError("Failed to archive Multica agent", err.Error())
				return
			}
		} else if err := r.client.RestoreAgent(ctx, state.ID.ValueString()); err != nil {
			resp.Diagnostics.AddError("Failed to restore Multica agent", err.Error())
			return
		}
	}
	plan.ID = state.ID
	plan.ContentHash = types.StringValue(declarativeContentHashOrEmpty(plan.Config))
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *agentResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state agentResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.ArchiveAgent(ctx, state.ID.ValueString()); err != nil {
		if isArchivedAgentError(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to archive Multica agent", err.Error())
		return
	}
	resp.State.RemoveResource(ctx)
}

func isArchivedAgentError(err error) bool {
	var httpErr *client.HTTPError
	if !errors.As(err, &httpErr) {
		return false
	}
	if httpErr.StatusCode == http.StatusNotFound {
		return true
	}
	return httpErr.StatusCode == http.StatusConflict &&
		strings.Contains(strings.ToLower(httpErr.Body), "already archived")
}

func (r *agentResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

func validateAgentConfig(config agentConfigModel) error {
	if config.Name.IsNull() || config.Name.ValueString() == "" {
		return fmt.Errorf("config.name is required")
	}
	if config.Runtime.ID.IsNull() && config.Runtime.Name.IsNull() && config.Runtime.CustomName.IsNull() && config.Runtime.Provider.IsNull() && config.Runtime.Reference.IsNull() {
		return fmt.Errorf("config.runtime must contain at least one selector: id, name, custom_name, or provider")
	}
	if len(config.UnsupportedFields) > 0 {
		return fmt.Errorf("unsupported declarative fields: %s", strings.Join(config.UnsupportedFields, ", "))
	}
	if !config.PermissionMode.IsNull() && config.PermissionMode.ValueString() != "private" && config.PermissionMode.ValueString() != "public_to" {
		return fmt.Errorf("config.permission_mode must be private or public_to")
	}
	return nil
}

func (r *agentResource) requestBody(ctx context.Context, config agentConfigModel) (map[string]any, []string, error) {
	runtimeID, err := r.resolveRuntime(ctx, config.Runtime)
	if err != nil {
		return nil, nil, err
	}
	body := map[string]any{
		"name":       config.Name.ValueString(),
		"runtime_id": runtimeID,
	}
	putString(body, "description", config.Description)
	putString(body, "instructions", config.Instructions)
	putString(body, "avatar_url", config.AvatarURL)
	putString(body, "model", config.Model)
	putString(body, "thinking_level", config.ThinkingLevel)
	putString(body, "permission_mode", config.PermissionMode)
	putString(body, "visibility", config.Visibility)
	if !config.RuntimeConfig.IsNull() && !config.RuntimeConfig.IsUnknown() {
		var value any
		if err := json.Unmarshal([]byte(config.RuntimeConfig.ValueString()), &value); err != nil {
			return nil, nil, fmt.Errorf("config.runtime_config must be valid JSON: %w", err)
		}
		body["runtime_config"] = value
	}
	if !config.MCPConfig.IsNull() && !config.MCPConfig.IsUnknown() {
		var value any
		if err := json.Unmarshal([]byte(config.MCPConfig.ValueString()), &value); err != nil {
			return nil, nil, fmt.Errorf("config.mcp_config must be valid JSON: %w", err)
		}
		body["mcp_config"] = value
	}
	if !config.MaxConcurrentTasks.IsNull() && !config.MaxConcurrentTasks.IsUnknown() {
		body["max_concurrent_tasks"] = config.MaxConcurrentTasks.ValueInt64()
	}
	if !config.CustomArgs.IsNull() && !config.CustomArgs.IsUnknown() {
		var args []string
		if diags := config.CustomArgs.ElementsAs(ctx, &args, false); diags.HasError() {
			return nil, nil, fmt.Errorf("config.custom_args: %v", diags)
		}
		body["custom_args"] = args
	}
	targets, err := invocationTargets(ctx, config.InvocationTargets)
	if err != nil {
		return nil, nil, err
	}
	if targets != nil {
		body["invocation_targets"] = targets
	}
	if !config.ComposioToolkitAllowlist.IsNull() && !config.ComposioToolkitAllowlist.IsUnknown() {
		var allowlist []string
		if diags := config.ComposioToolkitAllowlist.ElementsAs(ctx, &allowlist, false); diags.HasError() {
			return nil, nil, fmt.Errorf("config.composioToolkitAllowlist: %v", diags)
		}
		body["composio_toolkit_allowlist"] = allowlist
	}
	ids, err := r.resolveSkills(ctx, config.Skills)
	return body, ids, err
}

func (r *agentResource) resolveRuntime(ctx context.Context, selector runtimeSelectorModel) (string, error) {
	if !selector.ID.IsNull() && selector.ID.ValueString() != "" {
		return selector.ID.ValueString(), nil
	}
	runtimes, err := r.client.ListRuntimes(ctx)
	if err != nil {
		return "", fmt.Errorf("list runtimes: %w", err)
	}
	var matches []client.Runtime
	for _, runtime := range runtimes {
		if !selector.Reference.IsNull() && selector.Reference.ValueString() != "" {
			reference := selector.Reference.ValueString()
			if reference != runtime.ID && reference != runtime.Name && reference != runtime.CustomName && reference != runtime.Provider {
				continue
			}
		}
		if !selectorMatchesRuntime(selector, runtime) {
			continue
		}
		matches = append(matches, runtime)
	}
	if len(matches) != 1 {
		return "", fmt.Errorf("runtime selector matched %d runtimes; it must match exactly one", len(matches))
	}
	return matches[0].ID, nil
}

func selectorMatchesRuntime(selector runtimeSelectorModel, runtime client.Runtime) bool {
	checks := []struct {
		value  types.String
		actual string
	}{
		{selector.Name, runtime.Name},
		{selector.CustomName, runtime.CustomName},
		{selector.Provider, runtime.Provider},
	}
	for _, check := range checks {
		if !check.value.IsNull() && !check.value.IsUnknown() && check.value.ValueString() != "" && check.value.ValueString() != check.actual {
			return false
		}
	}
	return true
}

func (r *agentResource) resolveSkills(ctx context.Context, values types.Set) ([]string, error) {
	if values.IsNull() || values.IsUnknown() {
		return []string{}, nil
	}
	var refs []string
	if diags := values.ElementsAs(ctx, &refs, false); diags.HasError() {
		return nil, fmt.Errorf("config.skills: %v", diags)
	}
	if len(refs) == 0 {
		return []string{}, nil
	}
	skills, err := r.client.ListSkills(ctx)
	if err != nil {
		return nil, fmt.Errorf("list skills: %w", err)
	}
	ids := make([]string, 0, len(refs))
	for _, ref := range refs {
		found := ""
		for _, skill := range skills {
			if skill.ID == ref || skill.Name == ref || skillSourceURL(skill) == ref {
				found = skill.ID
				break
			}
		}
		if found == "" {
			if !isRemoteSkillReference(ref) {
				return nil, fmt.Errorf("skill %q was not found in the workspace", ref)
			}

			imported, err := r.client.ImportSkill(ctx, ref, "skip")
			if err != nil {
				return nil, fmt.Errorf("import skill %q: %w", ref, err)
			}
			if imported.Skill != nil {
				found = imported.Skill.ID
			} else if imported.ExistingSkill != nil {
				found = imported.ExistingSkill.ID
			}
			if found == "" {
				return nil, fmt.Errorf("import skill %q returned no skill ID", ref)
			}
		}
		ids = append(ids, found)
	}
	return ids, nil
}

func isRemoteSkillReference(value string) bool {
	return strings.HasPrefix(value, "https://github.com/") ||
		strings.HasPrefix(value, "github.com/") ||
		strings.HasPrefix(value, "https://skills.sh/") ||
		strings.HasPrefix(value, "skills.sh/") ||
		strings.HasPrefix(value, "https://clawhub.ai/") ||
		strings.HasPrefix(value, "clawhub.ai/")
}

func skillSourceURL(skill client.Skill) string {
	config, ok := skill.Config.(map[string]any)
	if !ok {
		return ""
	}
	origin, ok := config["origin"].(map[string]any)
	if !ok {
		return ""
	}
	sourceURL, _ := origin["source_url"].(string)
	return sourceURL
}

func stateConfigFromAgent(current agentConfigModel, agent client.Agent) agentConfigModel {
	current.Name = types.StringValue(agent.Name)
	current.Description = types.StringValue(agent.Description)
	current.Instructions = types.StringValue(agent.Instructions)
	current.AvatarURL = stringOrNull(agent.AvatarURL)
	if current.Runtime.ID.IsNull() && current.Runtime.Name.IsNull() && current.Runtime.CustomName.IsNull() && current.Runtime.Provider.IsNull() && current.Runtime.Reference.IsNull() {
		current.Runtime.ID = types.StringValue(agent.RuntimeID)
	}
	if agent.RuntimeConfig != nil {
		if encoded, err := json.Marshal(agent.RuntimeConfig); err == nil {
			current.RuntimeConfig = types.StringValue(string(encoded))
		}
	}
	current.Model = types.StringValue(agent.Model)
	current.ThinkingLevel = types.StringValue(agent.ThinkingLevel)
	current.PermissionMode = types.StringValue(agent.PermissionMode)
	current.Visibility = types.StringValue(agent.Visibility)
	current.MaxConcurrentTasks = types.Int64Value(int64(agent.MaxConcurrentTasks))
	current.CustomArgs = stringListValue(agent.CustomArgs)
	if current.Skills.IsNull() || current.Skills.IsUnknown() {
		current.Skills = stringSetValue(skillNames(agent.Skills))
	}
	current.InvocationTargets = invocationTargetValue(agent.InvocationTargets)
	if !agent.ComposioAllowlistRedacted {
		current.ComposioToolkitAllowlist = stringSetValue(agent.ComposioAllowlist)
	}
	current.Archived = types.BoolValue(agent.ArchivedAt != nil)
	if !agent.MCPConfigRedacted && len(agent.MCPConfig) > 0 && string(agent.MCPConfig) != "null" {
		current.MCPConfig = types.StringValue(string(agent.MCPConfig))
	}
	return current
}

func skillNames(skills []client.Skill) []string {
	result := make([]string, 0, len(skills))
	for _, skill := range skills {
		if skill.Name != "" {
			result = append(result, skill.Name)
		} else {
			result = append(result, skill.ID)
		}
	}
	return result
}

func preserveSkillRefs(configured any, skills []client.Skill) []string {
	refs, _ := configured.([]any)
	result := make([]string, 0, len(skills))
	matched := make(map[int]bool, len(skills))
	for _, raw := range refs {
		ref, ok := raw.(string)
		if !ok {
			continue
		}
		for index, skill := range skills {
			if matched[index] {
				continue
			}
			if skill.ID == ref || skill.Name == ref || skillSourceURL(skill) == ref {
				result = append(result, ref)
				matched[index] = true
				break
			}
		}
	}
	for index, skill := range skills {
		if matched[index] {
			continue
		}
		if skill.Name != "" {
			result = append(result, skill.Name)
		} else if skill.ID != "" {
			result = append(result, skill.ID)
		}
	}
	return result
}

func putString(body map[string]any, key string, value types.String) {
	if !value.IsNull() && !value.IsUnknown() {
		body[key] = value.ValueString()
	}
}

func stringMap(ctx context.Context, value types.Map) (map[string]string, error) {
	result := map[string]string{}
	if value.IsNull() || value.IsUnknown() {
		return result, nil
	}
	if diags := value.ElementsAs(ctx, &result, false); diags.HasError() {
		return nil, fmt.Errorf("%v", diags)
	}
	return result, nil
}

func invocationTargets(ctx context.Context, value types.Set) ([]client.InvocationTarget, error) {
	if value.IsNull() || value.IsUnknown() {
		return nil, nil
	}
	var models []invocationTargetModel
	if diags := value.ElementsAs(ctx, &models, false); diags.HasError() {
		return nil, fmt.Errorf("config.invocation_targets: %v", diags)
	}
	result := make([]client.InvocationTarget, 0, len(models))
	for _, model := range models {
		if model.TargetType.IsNull() || model.TargetType.ValueString() == "" {
			return nil, fmt.Errorf("config.invocation_targets.target_type is required")
		}
		target := client.InvocationTarget{TargetType: model.TargetType.ValueString()}
		if !model.TargetID.IsNull() {
			target.TargetID = model.TargetID.ValueString()
		}
		result = append(result, target)
	}
	return result, nil
}

func stringOrNull(value *string) types.String {
	if value == nil {
		return types.StringNull()
	}
	return types.StringValue(*value)
}

func stringListValue(values []string) types.List {
	return types.ListValueMust(types.StringType, stringValues(values))
}

// The state helpers are intentionally kept small in this first slice. The
// provider preserves write-only env and redacted MCP values from prior state;
// a later schema version will add explicit secret-reference attributes.
func stringSetValue(values []string) types.Set {
	return types.SetValueMust(types.StringType, stringValues(values))
}

func stringValues(values []string) []attr.Value {
	result := make([]attr.Value, len(values))
	for i, value := range values {
		result[i] = types.StringValue(value)
	}
	return result
}
