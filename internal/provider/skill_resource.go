package provider

import (
	"context"
	"errors"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/xiehengjian/terraform-provider-multica/internal/client"
)

var _ resource.Resource = (*skillResource)(nil)
var _ resource.ResourceWithConfigure = (*skillResource)(nil)

func newSkillResource() resource.Resource {
	return &skillResource{}
}

type skillResource struct {
	client *client.Client
}

type skillResourceModel struct {
	ID          types.String  `tfsdk:"id"`
	Name        types.String  `tfsdk:"name"`
	Description types.String  `tfsdk:"description"`
	Content     types.String  `tfsdk:"content"`
	Config      types.Dynamic `tfsdk:"config"`
	Files       types.Set     `tfsdk:"files"`
	SourceURL   types.String  `tfsdk:"source_url"`
}

type skillFileModel struct {
	Path    types.String `tfsdk:"path"`
	Content types.String `tfsdk:"content"`
}

func (r *skillResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_skill"
}

func (r *skillResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
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
				Description: "Workspace-unique skill name.",
			},
			"description": schema.StringAttribute{Optional: true},
			"content": schema.StringAttribute{
				Optional:    true,
				Description: "The primary SKILL.md content. Required unless source_url is set.",
			},
			"config": schema.DynamicAttribute{
				Optional:    true,
				Computed:    true,
				Description: "Arbitrary skill metadata, including import provenance.",
			},
			"files": schema.SetNestedAttribute{
				Optional:    true,
				Description: "Supporting text files. SKILL.md is represented by content and is not allowed here.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"path":    schema.StringAttribute{Required: true},
						"content": schema.StringAttribute{Required: true},
					},
				},
			},
			"source_url": schema.StringAttribute{
				Optional:    true,
				Description: "GitHub, Skills.sh, or ClawHub URL to import and keep in sync.",
			},
		},
	}
}

func (r *skillResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *skillResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan skillResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := validateSkillPlan(ctx, plan); err != nil {
		resp.Diagnostics.AddError("Invalid Multica skill", err.Error())
		return
	}

	var skill client.Skill
	var err error
	if sourceURL := stringValueOrEmpty(plan.SourceURL); sourceURL != "" {
		result, importErr := r.client.ImportSkill(ctx, sourceURL, "fail")
		if importErr != nil {
			resp.Diagnostics.AddError("Failed to import Multica skill", importErr.Error())
			return
		}
		if result.Skill == nil {
			resp.Diagnostics.AddError("Failed to import Multica skill", "the import response did not include a skill")
			return
		}
		skill = *result.Skill
	} else {
		skill, err = r.client.CreateSkill(ctx, skillRequestBody(ctx, plan))
		if err != nil {
			resp.Diagnostics.AddError("Failed to create Multica skill", err.Error())
			return
		}
	}

	state := skillStateFromAPI(plan, skill)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *skillResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state skillResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	skill, err := r.client.GetSkill(ctx, state.ID.ValueString())
	if err != nil {
		var httpErr *client.HTTPError
		if errors.As(err, &httpErr) && httpErr.StatusCode == 404 {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to read Multica skill", err.Error())
		return
	}

	state = skillStateFromAPI(state, skill)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *skillResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan skillResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := validateSkillPlan(ctx, plan); err != nil {
		resp.Diagnostics.AddError("Invalid Multica skill", err.Error())
		return
	}

	var skill client.Skill
	var err error
	if sourceURL := stringValueOrEmpty(plan.SourceURL); sourceURL != "" {
		result, importErr := r.client.ImportSkill(ctx, sourceURL, "overwrite")
		if importErr != nil {
			resp.Diagnostics.AddError("Failed to refresh imported Multica skill", importErr.Error())
			return
		}
		if result.Skill == nil {
			resp.Diagnostics.AddError("Failed to refresh imported Multica skill", "the import response did not include a skill")
			return
		}
		skill = *result.Skill
	} else {
		var state skillResourceModel
		resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
		if resp.Diagnostics.HasError() {
			return
		}
		skill, err = r.client.UpdateSkill(ctx, state.ID.ValueString(), skillRequestBody(ctx, plan))
		if err != nil {
			resp.Diagnostics.AddError("Failed to update Multica skill", err.Error())
			return
		}
	}

	state := skillStateFromAPI(plan, skill)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *skillResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state skillResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeleteSkill(ctx, state.ID.ValueString()); err != nil {
		var httpErr *client.HTTPError
		if errors.As(err, &httpErr) && httpErr.StatusCode == 404 {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to delete Multica skill", err.Error())
		return
	}
	resp.State.RemoveResource(ctx)
}

func (r *skillResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

func validateSkillPlan(ctx context.Context, plan skillResourceModel) error {
	if plan.Name.IsNull() || plan.Name.ValueString() == "" {
		return fmt.Errorf("name is required")
	}
	if stringValueOrEmpty(plan.SourceURL) == "" && plan.Content.IsNull() {
		return fmt.Errorf("content is required when source_url is not set")
	}
	if !plan.Files.IsNull() && !plan.Files.IsUnknown() {
		var files []skillFileModel
		if diags := plan.Files.ElementsAs(ctx, &files, false); diags.HasError() {
			return fmt.Errorf("files: %v", diags)
		}
		for _, file := range files {
			if file.Path.ValueString() == "SKILL.md" {
				return fmt.Errorf("files must not contain SKILL.md; use content")
			}
		}
	}
	return nil
}

func skillRequestBody(ctx context.Context, plan skillResourceModel) map[string]any {
	body := map[string]any{
		"name": plan.Name.ValueString(),
	}
	if !plan.Description.IsNull() && !plan.Description.IsUnknown() {
		body["description"] = plan.Description.ValueString()
	}
	if !plan.Content.IsNull() && !plan.Content.IsUnknown() {
		body["content"] = plan.Content.ValueString()
	}
	if !plan.Config.IsNull() && !plan.Config.IsUnknown() {
		if config, err := attrToGo(plan.Config); err == nil {
			body["config"] = config
		}
	}
	if !plan.Files.IsNull() && !plan.Files.IsUnknown() {
		body["files"] = skillFilesFromTerraform(ctx, plan.Files)
	}
	return body
}

func skillFilesFromTerraform(ctx context.Context, value types.Set) []map[string]string {
	var files []skillFileModel
	if diags := value.ElementsAs(ctx, &files, false); diags.HasError() {
		return nil
	}
	result := make([]map[string]string, 0, len(files))
	for _, file := range files {
		result = append(result, map[string]string{
			"path":    file.Path.ValueString(),
			"content": file.Content.ValueString(),
		})
	}
	return result
}

func skillStateFromAPI(previous skillResourceModel, skill client.Skill) skillResourceModel {
	previous.ID = types.StringValue(skill.ID)
	previous.Name = types.StringValue(skill.Name)
	previous.Description = types.StringValue(skill.Description)
	previous.Content = types.StringValue(skill.Content)
	if skill.Config == nil || (isEmptyCollection(skill.Config) && previous.Config.IsNull()) {
		previous.Config = types.DynamicNull()
	} else {
		previous.Config = goToDynamic(skill.Config)
	}
	previous.Files = skillFilesValue(skill.Files)
	if sourceURL := skillSourceURL(skill); sourceURL != "" {
		previous.SourceURL = types.StringValue(sourceURL)
	}
	return previous
}

func skillFilesValue(files []client.SkillFile) types.Set {
	elements := make([]attr.Value, 0, len(files))
	for _, file := range files {
		elements = append(elements, types.ObjectValueMust(skillFileObjectType().AttrTypes, map[string]attr.Value{
			"path":    types.StringValue(file.Path),
			"content": types.StringValue(file.Content),
		}))
	}
	return types.SetValueMust(skillFileObjectType(), elements)
}

func skillFileObjectType() types.ObjectType {
	return types.ObjectType{AttrTypes: map[string]attr.Type{
		"path":    types.StringType,
		"content": types.StringType,
	}}
}

func stringValueOrEmpty(value types.String) string {
	if value.IsNull() || value.IsUnknown() {
		return ""
	}
	return value.ValueString()
}
