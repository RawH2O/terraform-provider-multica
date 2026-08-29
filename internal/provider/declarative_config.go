package provider

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/xiehengjian/terraform-provider-multica/internal/client"
)

// decodeDeclarativeConfig translates the multica-declarative v1alpha1 agent
// shape into the normalized API model used by the resource implementation.
// The outer Terraform attribute is Dynamic because the declarative format has
// compact/expanded unions (model, skills, permission) and arbitrary runtime
// configuration.
func decodeDeclarativeConfig(ctx context.Context, value types.Dynamic) (agentConfigModel, error) {
	if value.IsNull() || value.IsUnknown() || value.UnderlyingValue() == nil {
		return agentConfigModel{}, fmt.Errorf("config must be a known YAML object")
	}
	raw, err := attrToGo(value.UnderlyingValue())
	if err != nil {
		return agentConfigModel{}, fmt.Errorf("config: %w", err)
	}
	root, ok := raw.(map[string]any)
	if !ok {
		return agentConfigModel{}, fmt.Errorf("config must be a YAML object")
	}

	config := agentConfigModel{}
	config.Name = stringValue(root, "name")
	config.Description = stringValue(root, "description")
	config.Instructions = stringValue(root, "instructions")
	if config.Instructions.IsNull() {
		if path := stringValue(root, "instructionsFile"); !path.IsNull() && path.ValueString() != "" {
			data, err := readDeclarativeFile(path.ValueString())
			if err != nil {
				return agentConfigModel{}, fmt.Errorf("instructionsFile: %w", err)
			}
			config.Instructions = types.StringValue(string(data))
		}
	}

	if model, exists := root["model"]; exists {
		modelID, err := modelID(model)
		if err != nil {
			return agentConfigModel{}, fmt.Errorf("model: %w", err)
		}
		config.Model = types.StringValue(modelID)
	}

	if skills, exists := root["skills"]; exists {
		refs, err := skillRefs(skills)
		if err != nil {
			return agentConfigModel{}, fmt.Errorf("skills: %w", err)
		}
		config.Skills = stringSetValue(refs)
	}
	if hooks, exists := root["hooks"]; exists {
		refs, err := stringRefs(hooks)
		if err != nil {
			return agentConfigModel{}, fmt.Errorf("hooks: %w", err)
		}
		config.Hooks = stringSetValue(refs)
	}

	multica, ok := objectValue(root, "multica")
	if !ok {
		return agentConfigModel{}, fmt.Errorf("multica is required")
	}
	if runtime, exists := multica["runtime"]; exists {
		selector, err := runtimeSelector(runtime)
		if err != nil {
			return agentConfigModel{}, fmt.Errorf("multica.runtime: %w", err)
		}
		config.Runtime = selector
	} else {
		return agentConfigModel{}, fmt.Errorf("multica.runtime is required")
	}

	if runtimeConfig, exists := multica["runtimeConfig"]; exists {
		encoded, err := jsonString(runtimeConfig)
		if err != nil {
			return agentConfigModel{}, fmt.Errorf("multica.runtimeConfig: %w", err)
		}
		config.RuntimeConfig = types.StringValue(encoded)
	}
	config.ThinkingLevel = stringValue(multica, "thinkingLevel")
	config.MaxConcurrentTasks = intValue(multica, "maxConcurrentTasks")
	config.CustomArgs, err = stringList(multica, "customArgs")
	if err != nil {
		return agentConfigModel{}, fmt.Errorf("multica.customArgs: %w", err)
	}
	config.PermissionMode, config.InvocationTargets, err = permission(multica)
	if err != nil {
		return agentConfigModel{}, fmt.Errorf("multica.permission: %w", err)
	}

	if envFile := stringValue(multica, "customEnvFile"); !envFile.IsNull() && envFile.ValueString() != "" {
		env, err := readCustomEnvFile(envFile.ValueString())
		if err != nil {
			return agentConfigModel{}, fmt.Errorf("customEnvFile: %w", err)
		}
		mapped, diags := types.MapValueFrom(ctx, types.StringType, env)
		if diags.HasError() {
			return agentConfigModel{}, fmt.Errorf("customEnvFile: %v", diags)
		}
		config.CustomEnv = mapped
	} else if env, exists := multica["customEnv"]; exists {
		_ = env
		return agentConfigModel{}, fmt.Errorf("multica.customEnv is not accepted; use customEnvFile so secrets do not enter Terraform state")
	}

	if mcpFile := stringValue(multica, "mcpConfigFile"); !mcpFile.IsNull() && mcpFile.ValueString() != "" {
		data, err := readDeclarativeFile(mcpFile.ValueString())
		if err != nil {
			return agentConfigModel{}, fmt.Errorf("mcpConfigFile: %w", err)
		}
		var decoded any
		if err := json.Unmarshal(data, &decoded); err != nil {
			return agentConfigModel{}, fmt.Errorf("mcpConfigFile must contain valid JSON: %w", err)
		}
		config.MCPConfig = types.StringValue(string(data))
	} else if mcp, exists := multica["mcpConfig"]; exists {
		_ = mcp
		return agentConfigModel{}, fmt.Errorf("multica.mcpConfig is not accepted; use mcpConfigFile so secrets do not enter Terraform state")
	}

	if allowlist, exists := multica["composioToolkitAllowlist"]; exists {
		values, err := stringSlice(allowlist)
		if err != nil {
			return agentConfigModel{}, fmt.Errorf("multica.composioToolkitAllowlist: %w", err)
		}
		config.ComposioToolkitAllowlist = stringSetValue(values)
	}
	if archived, exists := multica["archived"]; exists {
		value, ok := archived.(bool)
		if !ok {
			return agentConfigModel{}, fmt.Errorf("multica.archived must be boolean")
		}
		config.Archived = types.BoolValue(value)
	}
	if avatar := stringValue(multica, "avatarFile"); !avatar.IsNull() && avatar.ValueString() != "" {
		config.UnsupportedFields = append(config.UnsupportedFields, "multica.avatarFile")
	}
	if disabled, exists := multica["disabledRuntimeSkills"]; exists && !isEmptyCollection(disabled) {
		config.UnsupportedFields = append(config.UnsupportedFields, "multica.disabledRuntimeSkills")
	}
	return config, nil
}

func modelID(value any) (string, error) {
	if id, ok := value.(string); ok {
		if id == "" {
			return "", fmt.Errorf("must not be empty")
		}
		return id, nil
	}
	obj, ok := value.(map[string]any)
	if !ok {
		return "", fmt.Errorf("must be a string or object with id")
	}
	id, ok := obj["id"].(string)
	if !ok || id == "" {
		return "", fmt.Errorf("object.id must be a non-empty string")
	}
	return id, nil
}

func skillRefs(value any) ([]string, error) {
	return stringRefs(value)
}

func stringRefs(value any) ([]string, error) {
	items, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("must be a list")
	}
	result := make([]string, 0, len(items))
	for i, item := range items {
		switch typed := item.(type) {
		case string:
			if typed != "" {
				result = append(result, typed)
			}
		case map[string]any:
			name, ok := typed["name"].(string)
			if !ok || name == "" {
				return nil, fmt.Errorf("item %d must contain name", i)
			}
			if enabled, exists := typed["enabled"]; exists {
				isEnabled, ok := enabled.(bool)
				if !ok {
					return nil, fmt.Errorf("item %d.enabled must be boolean", i)
				}
				if !isEnabled {
					continue
				}
			}
			result = append(result, name)
		default:
			return nil, fmt.Errorf("item %d must be a string or object", i)
		}
	}
	return result, nil
}

func runtimeSelector(value any) (runtimeSelectorModel, error) {
	selector := runtimeSelectorModel{}
	if reference, ok := value.(string); ok {
		if reference == "" {
			return selector, fmt.Errorf("must not be empty")
		}
		selector.Reference = types.StringValue(reference)
		return selector, nil
	}
	obj, ok := value.(map[string]any)
	if !ok {
		return selector, fmt.Errorf("must be a string or selector object")
	}
	selector.ID = stringValue(obj, "id")
	selector.Name = stringValue(obj, "name")
	selector.CustomName = stringValue(obj, "customName")
	selector.Provider = stringValue(obj, "provider")
	if selector.ID.IsNull() && selector.Name.IsNull() && selector.CustomName.IsNull() && selector.Provider.IsNull() {
		return selector, fmt.Errorf("must contain id, name, customName, or provider")
	}
	return selector, nil
}

func permission(multica map[string]any) (types.String, types.Set, error) {
	value, exists := multica["permission"]
	if !exists {
		return types.StringNull(), types.SetNull(invocationTargetObjectType()), nil
	}
	if mode, ok := value.(string); ok {
		switch mode {
		case "private":
			return types.StringValue("private"), invocationTargetValue(nil), nil
		case "workspace":
			return types.StringValue("public_to"), invocationTargetValue([]client.InvocationTarget{{TargetType: "workspace"}}), nil
		case "public_to":
			return types.StringValue("public_to"), invocationTargetValue(nil), nil
		default:
			return types.StringNull(), types.SetNull(invocationTargetObjectType()), fmt.Errorf("must be private, workspace, or public_to")
		}
	}
	obj, ok := value.(map[string]any)
	if !ok {
		return types.StringNull(), types.SetNull(invocationTargetObjectType()), fmt.Errorf("must be a string or object")
	}
	mode := "private"
	if raw, exists := obj["mode"]; exists {
		var ok bool
		mode, ok = raw.(string)
		if !ok || (mode != "private" && mode != "public_to") {
			return types.StringNull(), types.SetNull(invocationTargetObjectType()), fmt.Errorf("mode must be private or public_to")
		}
	}
	targets := []client.InvocationTarget{}
	if workspace, exists := obj["workspace"]; exists {
		isWorkspace, ok := workspace.(bool)
		if !ok {
			return types.StringNull(), types.SetNull(invocationTargetObjectType()), fmt.Errorf("workspace must be boolean")
		}
		if isWorkspace {
			targets = append(targets, client.InvocationTarget{TargetType: "workspace"})
		}
	}
	if members, exists := obj["members"]; exists {
		refs, err := stringSlice(members)
		if err != nil {
			return types.StringNull(), types.SetNull(invocationTargetObjectType()), fmt.Errorf("members: %w", err)
		}
		for _, member := range refs {
			targets = append(targets, client.InvocationTarget{TargetType: "member", TargetID: member})
		}
	}
	if mode == "private" {
		targets = nil
	}
	return types.StringValue(mode), invocationTargetValue(targets), nil
}

func invocationTargetObjectType() types.ObjectType {
	return types.ObjectType{AttrTypes: map[string]attr.Type{
		"target_type": types.StringType,
		"target_id":   types.StringType,
	}}
}

func invocationTargetValue(values []client.InvocationTarget) types.Set {
	objectTypes := invocationTargetObjectType().AttrTypes
	if values == nil {
		return types.SetNull(invocationTargetObjectType())
	}
	items := make([]attr.Value, 0, len(values))
	for _, value := range values {
		id := types.StringNull()
		if value.TargetID != "" {
			id = types.StringValue(value.TargetID)
		}
		items = append(items, types.ObjectValueMust(objectTypes, map[string]attr.Value{
			"target_type": types.StringValue(value.TargetType),
			"target_id":   id,
		}))
	}
	return types.SetValueMust(invocationTargetObjectType(), items)
}

func stringList(values map[string]any, key string) (types.List, error) {
	value, exists := values[key]
	if !exists {
		return types.ListNull(types.StringType), nil
	}
	items, err := stringSlice(value)
	if err != nil {
		return types.ListNull(types.StringType), err
	}
	return stringListValue(items), nil
}

func stringSlice(value any) ([]string, error) {
	items, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("must be a list of strings")
	}
	result := make([]string, 0, len(items))
	for i, item := range items {
		text, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("item %d must be a string", i)
		}
		result = append(result, text)
	}
	return result, nil
}

func stringMapValue(value any) (map[string]string, error) {
	obj, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("must be an object")
	}
	result := make(map[string]string, len(obj))
	for key, raw := range obj {
		text, ok := raw.(string)
		if !ok {
			return nil, fmt.Errorf("value for %q must be a string", key)
		}
		result[key] = text
	}
	return result, nil
}

func jsonString(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func readCustomEnvFile(name string) (map[string]string, error) {
	data, err := readDeclarativeFile(name)
	if err != nil {
		return nil, err
	}
	var result map[string]string
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("must contain a JSON object of string values: %w", err)
	}
	if result == nil {
		result = map[string]string{}
	}
	return result, nil
}

func readDeclarativeFile(name string) ([]byte, error) {
	clean := filepath.Clean(name)
	if filepath.IsAbs(name) || clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("path must be relative and stay inside the Terraform working directory")
	}
	return os.ReadFile(clean)
}

func objectValue(values map[string]any, key string) (map[string]any, bool) {
	value, exists := values[key]
	if !exists {
		return nil, false
	}
	result, ok := value.(map[string]any)
	return result, ok
}

func stringValue(values map[string]any, key string) types.String {
	value, exists := values[key]
	if !exists || value == nil {
		return types.StringNull()
	}
	text, ok := value.(string)
	if !ok {
		return types.StringNull()
	}
	return types.StringValue(text)
}

func intValue(values map[string]any, key string) types.Int64 {
	value, exists := values[key]
	if !exists || value == nil {
		return types.Int64Null()
	}
	switch typed := value.(type) {
	case int:
		return types.Int64Value(int64(typed))
	case int64:
		return types.Int64Value(typed)
	case float64:
		return types.Int64Value(int64(typed))
	case *big.Float:
		result, _ := typed.Int64()
		return types.Int64Value(result)
	default:
		return types.Int64Null()
	}
}

func isEmptyCollection(value any) bool {
	switch typed := value.(type) {
	case []any:
		return len(typed) == 0
	case map[string]any:
		return len(typed) == 0
	default:
		return false
	}
}

// attrToGo is deliberately local to this provider. It converts Terraform's
// typed values into the ordinary map/list/scalar shape that mirrors YAML.
func attrToGo(value attr.Value) (any, error) {
	switch typed := value.(type) {
	case types.Dynamic:
		if typed.IsNull() || typed.IsUnknown() {
			return nil, nil
		}
		return attrToGo(typed.UnderlyingValue())
	case types.String:
		if typed.IsNull() || typed.IsUnknown() {
			return nil, nil
		}
		return typed.ValueString(), nil
	case types.Bool:
		if typed.IsNull() || typed.IsUnknown() {
			return nil, nil
		}
		return typed.ValueBool(), nil
	case types.Int64:
		if typed.IsNull() || typed.IsUnknown() {
			return nil, nil
		}
		return typed.ValueInt64(), nil
	case types.Number:
		if typed.IsNull() || typed.IsUnknown() {
			return nil, nil
		}
		value, _ := typed.ValueBigFloat().Float64()
		return value, nil
	case types.Object:
		if typed.IsNull() || typed.IsUnknown() {
			return nil, nil
		}
		result := make(map[string]any, len(typed.Attributes()))
		for key, item := range typed.Attributes() {
			converted, err := attrToGo(item)
			if err != nil {
				return nil, err
			}
			result[key] = converted
		}
		return result, nil
	case types.Map:
		if typed.IsNull() || typed.IsUnknown() {
			return nil, nil
		}
		result := make(map[string]any, len(typed.Elements()))
		for key, item := range typed.Elements() {
			converted, err := attrToGo(item)
			if err != nil {
				return nil, err
			}
			result[key] = converted
		}
		return result, nil
	case types.List:
		return attrSliceToGo(typed.Elements())
	case types.Set:
		return attrSliceToGo(typed.Elements())
	case types.Tuple:
		return attrSliceToGo(typed.Elements())
	default:
		return nil, fmt.Errorf("unsupported Terraform value type %T", value)
	}
}

func attrSliceToGo(values []attr.Value) ([]any, error) {
	result := make([]any, 0, len(values))
	for _, value := range values {
		converted, err := attrToGo(value)
		if err != nil {
			return nil, err
		}
		result = append(result, converted)
	}
	return result, nil
}

func encodeDeclarativeConfig(config agentConfigModel) types.Dynamic {
	root := map[string]any{
		"name": config.Name.ValueString(),
	}
	if !config.Description.IsNull() {
		root["description"] = config.Description.ValueString()
	}
	if !config.Instructions.IsNull() {
		root["instructions"] = config.Instructions.ValueString()
	}
	if !config.Model.IsNull() {
		root["model"] = map[string]any{"id": config.Model.ValueString()}
	}
	if !config.Skills.IsNull() {
		var skills []string
		_ = config.Skills.ElementsAs(context.Background(), &skills, false)
		root["skills"] = skills
	}
	if !config.Hooks.IsNull() {
		var hooks []string
		_ = config.Hooks.ElementsAs(context.Background(), &hooks, false)
		root["hooks"] = hooks
	}
	multica := map[string]any{}
	multica["runtime"] = runtimeReference(config.Runtime)
	if !config.RuntimeConfig.IsNull() {
		var value any
		if json.Unmarshal([]byte(config.RuntimeConfig.ValueString()), &value) == nil {
			multica["runtimeConfig"] = value
		}
	}
	if !config.ThinkingLevel.IsNull() {
		multica["thinkingLevel"] = config.ThinkingLevel.ValueString()
	}
	if !config.MaxConcurrentTasks.IsNull() {
		multica["maxConcurrentTasks"] = config.MaxConcurrentTasks.ValueInt64()
	}
	if !config.CustomArgs.IsNull() {
		var args []string
		_ = config.CustomArgs.ElementsAs(context.Background(), &args, false)
		multica["customArgs"] = args
	}
	if !config.PermissionMode.IsNull() {
		multica["permission"] = config.PermissionMode.ValueString()
	}
	if !config.ComposioToolkitAllowlist.IsNull() {
		var values []string
		_ = config.ComposioToolkitAllowlist.ElementsAs(context.Background(), &values, false)
		multica["composioToolkitAllowlist"] = values
	}
	if !config.Archived.IsNull() {
		multica["archived"] = config.Archived.ValueBool()
	}
	root["multica"] = multica
	return goToDynamic(root)
}

func runtimeReference(selector runtimeSelectorModel) any {
	if !selector.Reference.IsNull() {
		return selector.Reference.ValueString()
	}
	if !selector.ID.IsNull() {
		return map[string]any{"id": selector.ID.ValueString()}
	}
	if !selector.Name.IsNull() {
		return map[string]any{"name": selector.Name.ValueString()}
	}
	if !selector.CustomName.IsNull() {
		return map[string]any{"customName": selector.CustomName.ValueString()}
	}
	return map[string]any{"provider": selector.Provider.ValueString()}
}

func goToDynamic(value any) types.Dynamic {
	return types.DynamicValue(goToAttr(value))
}

func goToAttr(value any) attr.Value {
	switch typed := value.(type) {
	case nil:
		return types.DynamicNull()
	case string:
		return types.StringValue(typed)
	case bool:
		return types.BoolValue(typed)
	case int:
		return types.Int64Value(int64(typed))
	case int64:
		return types.Int64Value(typed)
	case float64:
		return types.NumberValue(big.NewFloat(typed))
	case *big.Float:
		return types.NumberValue(typed)
	case []string:
		items := make([]attr.Value, len(typed))
		for i, item := range typed {
			items[i] = types.StringValue(item)
		}
		return types.ListValueMust(types.StringType, items)
	case []any:
		items := make([]attr.Value, len(typed))
		typesForItems := make([]attr.Type, len(typed))
		for i, item := range typed {
			items[i] = goToAttr(item)
			typesForItems[i] = items[i].Type(context.Background())
		}
		return types.TupleValueMust(typesForItems, items)
	case map[string]any:
		attrs := make(map[string]attr.Value, len(typed))
		attrTypes := make(map[string]attr.Type, len(typed))
		for key, item := range typed {
			attrs[key] = goToAttr(item)
			attrTypes[key] = attrs[key].Type(context.Background())
		}
		return types.ObjectValueMust(attrTypes, attrs)
	default:
		return types.StringValue(fmt.Sprint(value))
	}
}

func mergeDeclarativeState(ctx context.Context, state types.Dynamic, agent client.Agent) (types.Dynamic, error) {
	raw, err := attrToGo(state.UnderlyingValue())
	if err != nil {
		return types.DynamicNull(), err
	}
	root, ok := raw.(map[string]any)
	if !ok {
		return types.DynamicNull(), fmt.Errorf("state config is not an object")
	}
	root["name"] = agent.Name
	if _, exists := root["description"]; exists {
		root["description"] = agent.Description
	}
	if _, hasFile := root["instructionsFile"]; !hasFile {
		if _, exists := root["instructions"]; exists {
			root["instructions"] = agent.Instructions
		}
	}
	if model, ok := root["model"].(map[string]any); ok {
		model["id"] = agent.Model
	} else if _, exists := root["model"]; exists {
		root["model"] = agent.Model
	}
	if _, exists := root["skills"]; exists {
		root["skills"] = preserveSkillRefs(root["skills"], agent.Skills)
	}
	if _, exists := root["hooks"]; exists {
		root["hooks"] = preserveHookRefs(root["hooks"], agent.Hooks)
	}
	multica, ok := root["multica"].(map[string]any)
	if !ok {
		return types.DynamicNull(), fmt.Errorf("state config is missing multica")
	}
	if _, exists := multica["thinkingLevel"]; exists {
		multica["thinkingLevel"] = agent.ThinkingLevel
	}
	if _, exists := multica["maxConcurrentTasks"]; exists {
		multica["maxConcurrentTasks"] = int64(agent.MaxConcurrentTasks)
	}
	if _, exists := multica["customArgs"]; exists {
		multica["customArgs"] = agent.CustomArgs
	}
	if _, exists := multica["archived"]; exists {
		multica["archived"] = agent.ArchivedAt != nil
	}
	if _, exists := multica["permission"]; exists {
		if agent.PermissionMode == "private" {
			multica["permission"] = "private"
		} else {
			permission := map[string]any{"mode": "public_to"}
			for _, target := range agent.InvocationTargets {
				switch target.TargetType {
				case "workspace":
					permission["workspace"] = true
				case "member":
					members, _ := permission["members"].([]string)
					if target.TargetID != "" {
						members = append(members, target.TargetID)
					}
					permission["members"] = members
				}
			}
			multica["permission"] = permission
		}
	}
	if _, exists := multica["runtimeConfig"]; exists {
		multica["runtimeConfig"] = agent.RuntimeConfig
	}
	if !agent.MCPConfigRedacted {
		if _, exists := multica["mcpConfig"]; exists && len(agent.MCPConfig) > 0 {
			var value any
			if json.Unmarshal(agent.MCPConfig, &value) == nil {
				multica["mcpConfig"] = value
			}
		}
	}
	if !agent.ComposioAllowlistRedacted {
		if _, exists := multica["composioToolkitAllowlist"]; exists {
			multica["composioToolkitAllowlist"] = agent.ComposioAllowlist
		}
	}
	return goToDynamic(root), nil
}

func declarativeContentHash(value types.Dynamic) (string, error) {
	if value.IsNull() || value.IsUnknown() {
		return "", fmt.Errorf("config is null or unknown")
	}
	raw, err := attrToGo(value.UnderlyingValue())
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return "", fmt.Errorf("encode declaration: %w", err)
	}
	hash := sha256.New()
	_, _ = hash.Write(encoded)
	root, ok := raw.(map[string]any)
	if !ok {
		return "", fmt.Errorf("config must be an object")
	}
	multica, _ := root["multica"].(map[string]any)
	fileRefs := []struct {
		key  string
		path string
	}{
		{"instructionsFile", stringFromAny(root["instructionsFile"])},
		{"description_file", stringFromAny(root["description_file"])},
		{"customEnvFile", stringFromAny(multica["customEnvFile"])},
		{"mcpConfigFile", stringFromAny(multica["mcpConfigFile"])},
		{"avatarFile", stringFromAny(multica["avatarFile"])},
	}
	for _, ref := range fileRefs {
		if ref.path == "" {
			continue
		}
		data, err := readDeclarativeFile(ref.path)
		if err != nil {
			return "", fmt.Errorf("%s: %w", ref.key, err)
		}
		_, _ = hash.Write([]byte(ref.key))
		_, _ = hash.Write(data)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func declarativeContentHashOrEmpty(value types.Dynamic) string {
	hash, err := declarativeContentHash(value)
	if err != nil {
		return ""
	}
	return hash
}

func stringFromAny(value any) string {
	text, _ := value.(string)
	return text
}
