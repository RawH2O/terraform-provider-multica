package provider

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/xiehengjian/terraform-provider-multica/internal/client"
)

// The declarative repository already has stable YAML formats for squads and
// autopilots. Keep these resources dynamic, like multica_agent.config, so the
// provider can accept those files without inventing a second schema that
// would immediately drift from multica-declarative.
type configResourceModel struct {
	ID          types.String  `tfsdk:"id"`
	Config      types.Dynamic `tfsdk:"config"`
	ContentHash types.String  `tfsdk:"content_hash"`
}

func configResourceSchema(description string) schema.Schema {
	return schema.Schema{Attributes: map[string]schema.Attribute{
		"id": schema.StringAttribute{
			Computed: true,
			PlanModifiers: []planmodifier.String{
				stringplanmodifier.UseStateForUnknown(),
			},
		},
		"config": schema.DynamicAttribute{
			Required:    true,
			Description: description,
		},
		"content_hash": schema.StringAttribute{
			Computed: true,
			PlanModifiers: []planmodifier.String{
				stringplanmodifier.UseStateForUnknown(),
			},
			Description: "Hash of the declarative configuration and referenced files.",
		},
	}}
}

// ────────────────────────────────────────────────────────────────────────────
// Squad resource

var _ resource.Resource = (*squadResource)(nil)
var _ resource.ResourceWithConfigure = (*squadResource)(nil)
var _ resource.ResourceWithModifyPlan = (*squadResource)(nil)

type squadResource struct{ client *client.Client }

func newSquadResource() resource.Resource { return &squadResource{} }

func (r *squadResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_squad"
}

func (r *squadResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = configResourceSchema("multica-declarative squad YAML object")
}

func (r *squadResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *squadResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan configResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	config, err := configMap(plan.Config)
	if err != nil {
		resp.Diagnostics.AddError("Invalid squad config", err.Error())
		return
	}
	body, leaderID, err := r.squadBody(ctx, config)
	if err != nil {
		resp.Diagnostics.AddError("Invalid squad config", err.Error())
		return
	}
	var squad map[string]any
	if err := r.client.PostJSON(ctx, "/api/squads", body, &squad); err != nil {
		resp.Diagnostics.AddError("Failed to create Multica squad", err.Error())
		return
	}
	id, ok := stringField(squad, "id")
	if !ok {
		resp.Diagnostics.AddError("Failed to create Multica squad", "create response did not include an id")
		return
	}
	if err := r.syncSquadMembers(ctx, id, leaderID, config); err != nil {
		resp.Diagnostics.AddError("Failed to synchronize squad members", err.Error())
		return
	}
	if strings.EqualFold(stringFieldDefault(config, "status", "active"), "archived") {
		if err := r.client.DeleteJSON(ctx, "/api/squads/"+id); err != nil {
			resp.Diagnostics.AddError("Failed to archive Multica squad", err.Error())
			return
		}
	}
	plan.ID = types.StringValue(id)
	plan.ContentHash = types.StringValue(declarativeContentHashOrEmpty(plan.Config))
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *squadResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state configResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var squad map[string]any
	if err := r.client.GetJSON(ctx, "/api/squads/"+state.ID.ValueString(), &squad); err != nil {
		if isNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to read Multica squad", err.Error())
		return
	}
	if state.Config.IsNull() || state.Config.IsUnknown() {
		var members []map[string]any
		if err := r.client.GetJSON(ctx, "/api/squads/"+state.ID.ValueString()+"/members", &members); err == nil {
			squad["members"] = members
		}
		state.Config = goToDynamic(canonicalSquadConfig(squad))
	}
	state.ContentHash = types.StringValue(declarativeContentHashOrEmpty(state.Config))
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *squadResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan configResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var state configResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	config, err := configMap(plan.Config)
	if err != nil {
		resp.Diagnostics.AddError("Invalid squad config", err.Error())
		return
	}
	body, leaderID, err := r.squadBody(ctx, config)
	if err != nil {
		resp.Diagnostics.AddError("Invalid squad config", err.Error())
		return
	}
	if err := r.client.PutJSON(ctx, "/api/squads/"+state.ID.ValueString(), body, &map[string]any{}); err != nil {
		resp.Diagnostics.AddError("Failed to update Multica squad", err.Error())
		return
	}
	if err := r.syncSquadMembers(ctx, state.ID.ValueString(), leaderID, config); err != nil {
		resp.Diagnostics.AddError("Failed to synchronize squad members", err.Error())
		return
	}
	if strings.EqualFold(stringFieldDefault(config, "status", "active"), "archived") {
		if err := r.client.DeleteJSON(ctx, "/api/squads/"+state.ID.ValueString()); err != nil {
			resp.Diagnostics.AddError("Failed to archive Multica squad", err.Error())
			return
		}
	}
	plan.ID = state.ID
	plan.ContentHash = types.StringValue(declarativeContentHashOrEmpty(plan.Config))
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *squadResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state configResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeleteJSON(ctx, "/api/squads/"+state.ID.ValueString()); err != nil && !isNotFound(err) {
		resp.Diagnostics.AddError("Failed to archive Multica squad", err.Error())
		return
	}
	resp.State.RemoveResource(ctx)
}

func (r *squadResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

func (r *squadResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	setConfigContentHash(ctx, req, resp)
}

func (r *squadResource) squadBody(ctx context.Context, config map[string]any) (map[string]any, string, error) {
	name := stringFieldDefault(config, "name", "")
	if name == "" {
		return nil, "", fmt.Errorf("config.name is required")
	}
	leader, ok := config["leader"]
	if !ok {
		return nil, "", fmt.Errorf("config.leader is required")
	}
	leaderID, err := resolveAgentID(ctx, r.client, leader)
	if err != nil {
		return nil, "", fmt.Errorf("config.leader: %w", err)
	}
	body := map[string]any{"name": name, "leader_id": leaderID}
	for _, key := range []string{"description", "purpose", "instructions", "avatar_url"} {
		if value, ok := config[key]; ok {
			if key == "purpose" {
				body["description"] = value
				continue
			}
			body[key] = value
		}
	}
	if descriptionFile := stringFieldDefault(config, "description_file", ""); descriptionFile != "" {
		data, err := readDeclarativeFile(descriptionFile)
		if err != nil {
			return nil, "", fmt.Errorf("config.description_file: %w", err)
		}
		body["instructions"] = string(data)
	}
	return body, leaderID, nil
}

func (r *squadResource) syncSquadMembers(ctx context.Context, squadID, leaderID string, config map[string]any) error {
	desired := map[string]map[string]string{
		"agent:" + leaderID: {"member_type": "agent", "member_id": leaderID, "role": "leader"},
	}
	rawMembers, ok := config["members"].([]any)
	if !ok && config["members"] != nil {
		return fmt.Errorf("config.members must be a list")
	}
	for index, raw := range rawMembers {
		member, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("config.members[%d] must be an object", index)
		}
		memberType := stringFieldDefault(member, "member_type", "agent")
		ref, ok := member["agent"]
		if memberType == "member" {
			ref, ok = member["member"]
		}
		if !ok {
			ref = member["member_id"]
		}
		if !ok && ref == nil {
			return fmt.Errorf("config.members[%d] must contain agent or member", index)
		}
		memberID, err := resolveMemberID(ctx, r.client, memberType, ref)
		if err != nil {
			return fmt.Errorf("config.members[%d]: %w", index, err)
		}
		role := stringFieldDefault(member, "role", "member")
		desired[memberType+":"+memberID] = map[string]string{
			"member_type": memberType,
			"member_id":   memberID,
			"role":        role,
		}
	}

	var current []map[string]any
	if err := r.client.GetJSON(ctx, "/api/squads/"+squadID+"/members", &current); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, member := range current {
		memberType := stringFieldDefault(member, "member_type", "agent")
		memberID := stringFieldDefault(member, "member_id", "")
		key := memberType + ":" + memberID
		desiredMember, wanted := desired[key]
		if wanted {
			seen[key] = true
			if stringFieldDefault(member, "role", "") != desiredMember["role"] {
				if err := r.client.PatchJSON(ctx, "/api/squads/"+squadID+"/members/role", desiredMember, &map[string]any{}); err != nil {
					return err
				}
			}
			continue
		}
		if memberType == "agent" && memberID == leaderID {
			continue
		}
		if err := removeSquadMember(ctx, r.client, squadID, memberType, memberID); err != nil && !isNotFound(err) {
			return err
		}
	}
	for key, member := range desired {
		if seen[key] {
			continue
		}
		if err := r.client.PostJSON(ctx, "/api/squads/"+squadID+"/members", member, &map[string]any{}); err != nil {
			return err
		}
	}
	return nil
}

func removeSquadMember(ctx context.Context, c *client.Client, squadID, memberType, memberID string) error {
	return c.DeleteJSONWithBody(ctx, "/api/squads/"+squadID+"/members", map[string]any{
		"member_type": memberType,
		"member_id":   memberID,
	})
}

// ────────────────────────────────────────────────────────────────────────────
// Autopilot resource

var _ resource.Resource = (*autopilotResource)(nil)
var _ resource.ResourceWithConfigure = (*autopilotResource)(nil)
var _ resource.ResourceWithModifyPlan = (*autopilotResource)(nil)

type autopilotResource struct{ client *client.Client }

func newAutopilotResource() resource.Resource { return &autopilotResource{} }

func (r *autopilotResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_autopilot"
}

func (r *autopilotResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = configResourceSchema("multica-declarative autopilot YAML object")
}

func (r *autopilotResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *autopilotResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan configResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	config, err := configMap(plan.Config)
	if err != nil {
		resp.Diagnostics.AddError("Invalid autopilot config", err.Error())
		return
	}
	body, err := r.autopilotBody(ctx, config)
	if err != nil {
		resp.Diagnostics.AddError("Invalid autopilot config", err.Error())
		return
	}
	var result map[string]any
	if err := r.client.PostJSON(ctx, "/api/autopilots", body, &result); err != nil {
		resp.Diagnostics.AddError("Failed to create Multica autopilot", err.Error())
		return
	}
	id, ok := stringField(result, "id")
	if !ok {
		resp.Diagnostics.AddError("Failed to create Multica autopilot", "create response did not include an id")
		return
	}
	if err := r.syncAutopilotTriggers(ctx, id, config); err != nil {
		resp.Diagnostics.AddError("Failed to synchronize autopilot triggers", err.Error())
		return
	}
	if status := stringFieldDefault(config, "status", "active"); status != "" && status != "active" {
		if err := r.client.PatchJSON(ctx, "/api/autopilots/"+id, map[string]any{"status": status}, &map[string]any{}); err != nil {
			resp.Diagnostics.AddError("Failed to set Multica autopilot status", err.Error())
			return
		}
	}
	plan.ID = types.StringValue(id)
	plan.ContentHash = types.StringValue(declarativeContentHashOrEmpty(plan.Config))
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *autopilotResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state configResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	detail, err := r.client.GetAutopilot(ctx, state.ID.ValueString())
	if err != nil {
		if isNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to read Multica autopilot", err.Error())
		return
	}
	if state.Config.IsNull() || state.Config.IsUnknown() {
		state.Config = goToDynamic(canonicalAutopilotConfig(detail))
	}
	state.ContentHash = types.StringValue(declarativeContentHashOrEmpty(state.Config))
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *autopilotResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan configResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var state configResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	config, err := configMap(plan.Config)
	if err != nil {
		resp.Diagnostics.AddError("Invalid autopilot config", err.Error())
		return
	}
	body, err := r.autopilotBody(ctx, config)
	if err != nil {
		resp.Diagnostics.AddError("Invalid autopilot config", err.Error())
		return
	}
	if err := r.client.PatchJSON(ctx, "/api/autopilots/"+state.ID.ValueString(), body, &map[string]any{}); err != nil {
		resp.Diagnostics.AddError("Failed to update Multica autopilot", err.Error())
		return
	}
	if err := r.syncAutopilotTriggers(ctx, state.ID.ValueString(), config); err != nil {
		resp.Diagnostics.AddError("Failed to synchronize autopilot triggers", err.Error())
		return
	}
	plan.ID = state.ID
	plan.ContentHash = types.StringValue(declarativeContentHashOrEmpty(plan.Config))
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *autopilotResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state configResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeleteJSON(ctx, "/api/autopilots/"+state.ID.ValueString()); err != nil && !isNotFound(err) {
		resp.Diagnostics.AddError("Failed to delete Multica autopilot", err.Error())
		return
	}
	resp.State.RemoveResource(ctx)
}

func (r *autopilotResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

func (r *autopilotResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	setConfigContentHash(ctx, req, resp)
}

func (r *autopilotResource) autopilotBody(ctx context.Context, config map[string]any) (map[string]any, error) {
	title := stringFieldDefault(config, "title", "")
	if title == "" {
		return nil, fmt.Errorf("config.title is required")
	}
	mode := stringFieldDefault(config, "mode", stringFieldDefault(config, "execution_mode", ""))
	if mode != "create_issue" && mode != "run_only" {
		return nil, fmt.Errorf("config.mode must be create_issue or run_only")
	}
	ref, ok := config["agent"]
	assigneeType := stringFieldDefault(config, "assignee_type", "agent")
	if !ok {
		ref, ok = config["assignee"]
	}
	if !ok {
		return nil, fmt.Errorf("config.agent is required")
	}
	assigneeID, err := resolveAssigneeID(ctx, r.client, assigneeType, ref)
	if err != nil {
		return nil, fmt.Errorf("config.agent: %w", err)
	}
	body := map[string]any{
		"title":          title,
		"assignee_type":  assigneeType,
		"assignee_id":    assigneeID,
		"execution_mode": mode,
	}
	for _, key := range []string{"description", "project_id", "issue_title_template", "status"} {
		if value, ok := config[key]; ok {
			body[key] = value
		}
	}
	if descriptionFile := stringFieldDefault(config, "description_file", ""); descriptionFile != "" {
		data, err := readDeclarativeFile(descriptionFile)
		if err != nil {
			return nil, fmt.Errorf("config.description_file: %w", err)
		}
		body["description"] = string(data)
	}
	if project, ok := config["project"]; ok {
		projectID, err := resolveProjectID(ctx, r.client, project)
		if err != nil {
			return nil, fmt.Errorf("config.project: %w", err)
		}
		body["project_id"] = projectID
	}
	if subscribers, ok := config["subscribers"]; ok {
		body["subscribers"] = normalizeSubscribers(subscribers)
	}
	if priority := stringFieldDefault(config, "priority", "none"); priority != "" && priority != "none" {
		return nil, fmt.Errorf("config.priority is not supported by the Multica autopilot API; use none")
	}
	return body, nil
}

func (r *autopilotResource) syncAutopilotTriggers(ctx context.Context, autopilotID string, config map[string]any) error {
	detail, err := r.client.GetAutopilot(ctx, autopilotID)
	if err != nil {
		return err
	}
	current := objectList(detail["triggers"])
	desired := objectList(config["triggers"])
	currentByKey := map[string]map[string]any{}
	for _, trigger := range current {
		currentByKey[triggerKey(trigger)] = trigger
	}
	seen := map[string]bool{}
	for _, trigger := range desired {
		body, err := triggerBody(trigger)
		if err != nil {
			return err
		}
		key := triggerKey(trigger)
		if existing, ok := currentByKey[key]; ok {
			id, ok := stringField(existing, "id")
			if !ok {
				return fmt.Errorf("existing trigger %q has no id", key)
			}
			if err := r.client.PatchJSON(ctx, "/api/autopilots/"+autopilotID+"/triggers/"+id, body, &map[string]any{}); err != nil {
				return err
			}
			seen[key] = true
			continue
		}
		if err := r.client.PostJSON(ctx, "/api/autopilots/"+autopilotID+"/triggers", body, &map[string]any{}); err != nil {
			return err
		}
		seen[key] = true
	}
	for key, trigger := range currentByKey {
		if seen[key] {
			continue
		}
		id, ok := stringField(trigger, "id")
		if !ok {
			return fmt.Errorf("existing trigger %q has no id", key)
		}
		if err := r.client.DeleteJSON(ctx, "/api/autopilots/"+autopilotID+"/triggers/"+id); err != nil && !isNotFound(err) {
			return err
		}
	}
	return nil
}

func triggerBody(config map[string]any) (map[string]any, error) {
	kind := stringFieldDefault(config, "kind", "")
	if kind != "schedule" && kind != "webhook" {
		return nil, fmt.Errorf("trigger kind must be schedule or webhook")
	}
	body := map[string]any{"kind": kind}
	if value, ok := config["cron_expression"]; ok {
		body["cron_expression"] = value
	} else if value, ok := config["cron"]; ok {
		body["cron_expression"] = value
	}
	for _, key := range []string{"timezone", "label", "provider", "event_filters"} {
		if value, ok := config[key]; ok {
			body[key] = value
		}
	}
	if value, ok := config["enabled"]; ok {
		body["enabled"] = value
	}
	return body, nil
}

func triggerKey(config map[string]any) string {
	if label := stringFieldDefault(config, "label", ""); label != "" {
		return "label:" + label
	}
	return strings.Join([]string{
		stringFieldDefault(config, "kind", ""),
		stringFieldDefault(config, "cron_expression", stringFieldDefault(config, "cron", "")),
		stringFieldDefault(config, "timezone", ""),
	}, "|")
}

func canonicalSquadConfig(squad map[string]any) map[string]any {
	result := map[string]any{}
	for _, key := range []string{"name", "instructions", "avatar_url", "archived_at"} {
		if value, ok := squad[key]; ok {
			result[key] = value
		}
	}
	if description, ok := squad["description"]; ok {
		result["purpose"] = description
	}
	if leaderID, ok := squad["leader_id"]; ok {
		result["leader"] = leaderID
	}
	if members, ok := squad["members"]; ok {
		result["members"] = canonicalSquadMembers(members)
	}
	return result
}

func canonicalAutopilotConfig(detail map[string]any) map[string]any {
	ap, _ := detail["autopilot"].(map[string]any)
	if ap == nil {
		ap = detail
	}
	result := map[string]any{}
	for _, key := range []string{"title", "description", "status", "issue_title_template"} {
		if value, ok := ap[key]; ok {
			result[key] = value
		}
	}
	if value, ok := ap["project_id"]; ok {
		result["project"] = value
	}
	if value, ok := ap["assignee_id"]; ok {
		result["agent"] = value
	}
	if value, ok := ap["assignee_type"]; ok {
		result["assignee_type"] = value
	}
	if value, ok := ap["execution_mode"]; ok {
		result["mode"] = value
	}
	if triggers, ok := detail["triggers"]; ok {
		result["triggers"] = mapListToAny(triggers)
	}
	return result
}

func setConfigContentHash(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	// A resource imported into state but removed from Git has a null destroy
	// plan. Do not decode that null object into the config model.
	if req.Plan.Raw.IsNull() {
		return
	}
	var plan configResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() || plan.Config.IsNull() || plan.Config.IsUnknown() {
		return
	}
	hash, err := declarativeContentHash(plan.Config)
	if err != nil {
		resp.Diagnostics.AddError("Failed to hash declarative configuration", err.Error())
		return
	}
	if !req.State.Raw.IsNull() {
		var state configResourceModel
		resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
		if resp.Diagnostics.HasError() {
			return
		}
		if !state.ContentHash.IsNull() && !state.ContentHash.IsUnknown() && state.ContentHash.ValueString() == hash {
			return
		}
	}
	resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("content_hash"), types.StringValue(hash))...)
}

func configMap(value types.Dynamic) (map[string]any, error) {
	raw, err := attrToGo(value)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	result, ok := raw.(map[string]any)
	if !ok || result == nil {
		return nil, fmt.Errorf("config must be an object")
	}
	return result, nil
}

func resolveAgentID(ctx context.Context, c *client.Client, reference any) (string, error) {
	ref, ok := reference.(string)
	if !ok || strings.TrimSpace(ref) == "" {
		return "", fmt.Errorf("agent reference must be a non-empty string")
	}
	agents, err := c.ListAgents(ctx)
	if err != nil {
		return "", fmt.Errorf("list agents: %w", err)
	}
	for _, agent := range agents {
		if agent.ID == ref || agent.Name == ref {
			return agent.ID, nil
		}
	}
	return "", fmt.Errorf("agent %q was not found in the workspace", ref)
}

func resolveMemberID(ctx context.Context, c *client.Client, memberType string, reference any) (string, error) {
	if memberType == "agent" {
		return resolveAgentID(ctx, c, reference)
	}
	ref, ok := reference.(string)
	if !ok || strings.TrimSpace(ref) == "" {
		return "", fmt.Errorf("member reference must be a non-empty UUID")
	}
	return ref, nil
}

func resolveAssigneeID(ctx context.Context, c *client.Client, assigneeType string, reference any) (string, error) {
	if assigneeType == "agent" {
		return resolveAgentID(ctx, c, reference)
	}
	ref, ok := reference.(string)
	if !ok || strings.TrimSpace(ref) == "" {
		return "", fmt.Errorf("squad reference must be a non-empty string")
	}
	var squads []map[string]any
	if err := c.GetJSON(ctx, "/api/squads", &squads); err != nil {
		return "", fmt.Errorf("list squads: %w", err)
	}
	for _, squad := range squads {
		if id, ok := stringField(squad, "id"); ok && (id == ref || stringFieldDefault(squad, "name", "") == ref) {
			return id, nil
		}
	}
	return "", fmt.Errorf("squad %q was not found in the workspace", ref)
}

func resolveProjectID(ctx context.Context, c *client.Client, reference any) (string, error) {
	ref, ok := reference.(string)
	if !ok || strings.TrimSpace(ref) == "" {
		return "", fmt.Errorf("project reference must be a non-empty string")
	}
	var projects []map[string]any
	if err := c.GetJSON(ctx, "/api/projects", &projects); err != nil {
		return "", fmt.Errorf("list projects: %w", err)
	}
	for _, project := range projects {
		id, _ := stringField(project, "id")
		if id == ref || stringFieldDefault(project, "name", "") == ref || stringFieldDefault(project, "title", "") == ref {
			return id, nil
		}
	}
	return "", fmt.Errorf("project %q was not found in the workspace", ref)
}

func normalizeSubscribers(value any) []map[string]string {
	items := objectList(value)
	result := make([]map[string]string, 0, len(items))
	for _, item := range items {
		result = append(result, map[string]string{
			"user_type": stringFieldDefault(item, "user_type", "member"),
			"user_id":   stringFieldDefault(item, "user_id", stringFieldDefault(item, "member_id", "")),
		})
	}
	return result
}

func objectList(value any) []map[string]any {
	items := mapListToAny(value)
	result := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if object, ok := item.(map[string]any); ok {
			result = append(result, object)
		}
	}
	return result
}

func canonicalSquadMembers(value any) []any {
	items := mapListToAny(value)
	result := make([]any, 0, len(items))
	for _, raw := range items {
		member, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		entry := map[string]any{
			"role": stringFieldDefault(member, "role", "member"),
		}
		memberType := stringFieldDefault(member, "member_type", "agent")
		memberID := stringFieldDefault(member, "member_id", "")
		if memberType == "member" {
			entry["member"] = memberID
		} else {
			entry["agent"] = memberID
		}
		result = append(result, entry)
	}
	return result
}

func mapListToAny(value any) []any {
	switch items := value.(type) {
	case []any:
		return items
	case []map[string]any:
		result := make([]any, 0, len(items))
		for _, item := range items {
			result = append(result, item)
		}
		return result
	default:
		return nil
	}
}

func stringField(value map[string]any, key string) (string, bool) {
	text, ok := value[key].(string)
	return text, ok && text != ""
}

func stringFieldDefault(value map[string]any, key, fallback string) string {
	if text, ok := stringField(value, key); ok {
		return text
	}
	return fallback
}

func isNotFound(err error) bool {
	var httpErr *client.HTTPError
	return errors.As(err, &httpErr) && httpErr.StatusCode == 404
}
