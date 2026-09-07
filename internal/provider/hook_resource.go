package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/xiehengjian/terraform-provider-multica/internal/client"
)

var _ resource.Resource = (*hookResource)(nil)
var _ resource.ResourceWithConfigure = (*hookResource)(nil)

func newHookResource() resource.Resource {
	return &hookResource{}
}

type hookResource struct {
	client *client.Client
}

type hookResourceModel struct {
	ID          types.String  `tfsdk:"id"`
	Name        types.String  `tfsdk:"name"`
	Description types.String  `tfsdk:"description"`
	Command     types.String  `tfsdk:"command"`
	Providers   types.Set     `tfsdk:"providers"`
	Events      types.Set     `tfsdk:"events"`
	Matcher     types.String  `tfsdk:"matcher"`
	Config      types.Dynamic `tfsdk:"config"`
}

func (r *hookResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_hook"
}

func (r *hookResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Required:    true,
				Description: "Workspace-unique hook name.",
			},
			"description": schema.StringAttribute{Optional: true},
			"command": schema.StringAttribute{
				Required:    true,
				Description: "Executable command invoked by the agent hook runner.",
			},
			"providers": schema.SetAttribute{
				Optional:    true,
				Computed:    true,
				ElementType: types.StringType,
				Description: "Agent providers that can run this hook. Defaults to codex.",
			},
			"events": schema.SetAttribute{
				Required:    true,
				ElementType: types.StringType,
				Description: "Hook lifecycle events, for example Stop or UserPromptSubmit.",
			},
			"matcher": schema.StringAttribute{
				Optional:    true,
				Description: "Optional event payload matcher interpreted by the hook runner.",
			},
			"config": schema.DynamicAttribute{
				Optional:    true,
				Description: "Optional runner-specific hook configuration.",
			},
		},
	}
}

func (r *hookResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *hookResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan hookResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := validateHookPlan(ctx, plan); err != nil {
		resp.Diagnostics.AddError("Invalid Multica hook", err.Error())
		return
	}
	hook, err := r.client.CreateHook(ctx, hookRequestBody(ctx, plan))
	if err != nil {
		resp.Diagnostics.AddError("Failed to create Multica hook", err.Error())
		return
	}
	state := hookStateFromAPI(plan, hook)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *hookResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state hookResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	hook, err := r.client.GetHook(ctx, state.ID.ValueString())
	if err != nil {
		if isNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to read Multica hook", err.Error())
		return
	}
	state = hookStateFromAPI(state, hook)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *hookResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan hookResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := validateHookPlan(ctx, plan); err != nil {
		resp.Diagnostics.AddError("Invalid Multica hook", err.Error())
		return
	}
	var state hookResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	hook, err := r.client.UpdateHook(ctx, state.ID.ValueString(), hookRequestBody(ctx, plan))
	if err != nil {
		resp.Diagnostics.AddError("Failed to update Multica hook", err.Error())
		return
	}
	state = hookStateFromAPI(plan, hook)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *hookResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state hookResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeleteHook(ctx, state.ID.ValueString()); err != nil {
		if isNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to delete Multica hook", err.Error())
		return
	}
	resp.State.RemoveResource(ctx)
}

func (r *hookResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

func validateHookPlan(ctx context.Context, plan hookResourceModel) error {
	if plan.Name.IsNull() || (!plan.Name.IsUnknown() && plan.Name.ValueString() == "") {
		return fmt.Errorf("name is required")
	}
	if plan.Command.IsNull() || (!plan.Command.IsUnknown() && plan.Command.ValueString() == "") {
		return fmt.Errorf("command is required")
	}
	if plan.Events.IsNull() {
		return fmt.Errorf("events is required")
	}
	if plan.Events.IsUnknown() {
		return nil
	}
	events, err := hookStringSet(ctx, plan.Events, "events")
	if err != nil {
		return err
	}
	if len(events) == 0 {
		return fmt.Errorf("events must contain at least one event")
	}
	if !plan.Providers.IsNull() && !plan.Providers.IsUnknown() {
		providers, err := hookStringSet(ctx, plan.Providers, "providers")
		if err != nil {
			return err
		}
		if len(providers) == 0 {
			return fmt.Errorf("providers must contain at least one provider")
		}
	}
	return nil
}

func hookRequestBody(ctx context.Context, plan hookResourceModel) map[string]any {
	body := map[string]any{
		"name":    stringValueOrEmpty(plan.Name),
		"command": stringValueOrEmpty(plan.Command),
	}
	if !plan.Description.IsNull() && !plan.Description.IsUnknown() {
		body["description"] = plan.Description.ValueString()
	}
	if !plan.Providers.IsNull() && !plan.Providers.IsUnknown() {
		if providers, err := hookStringSet(ctx, plan.Providers, "providers"); err == nil {
			body["providers"] = providers
		}
	} else {
		body["providers"] = []string{"codex"}
	}
	if !plan.Events.IsNull() && !plan.Events.IsUnknown() {
		if events, err := hookStringSet(ctx, plan.Events, "events"); err == nil {
			body["events"] = events
		}
	}
	if !plan.Matcher.IsNull() && !plan.Matcher.IsUnknown() {
		body["matcher"] = plan.Matcher.ValueString()
	}
	if !plan.Config.IsNull() && !plan.Config.IsUnknown() {
		if config, err := attrToGo(plan.Config); err == nil {
			body["config"] = config
		}
	}
	return body
}

func hookStateFromAPI(previous hookResourceModel, hook client.Hook) hookResourceModel {
	previous.ID = types.StringValue(hook.ID)
	previous.Name = types.StringValue(hook.Name)
	previous.Description = types.StringValue(hook.Description)
	previous.Command = types.StringValue(hook.Command)
	previous.Providers = stringSetValue(hook.Providers)
	previous.Events = stringSetValue(hook.Events)
	previous.Matcher = types.StringValue(hook.Matcher)
	if hook.Config == nil || (isEmptyCollection(hook.Config) && previous.Config.IsNull()) {
		previous.Config = types.DynamicNull()
	} else {
		previous.Config = goToDynamic(hook.Config)
	}
	return previous
}

func hookStringSet(ctx context.Context, value types.Set, field string) ([]string, error) {
	var values []string
	if diags := value.ElementsAs(ctx, &values, false); diags.HasError() {
		return nil, fmt.Errorf("%s: %v", field, diags)
	}
	return values, nil
}
