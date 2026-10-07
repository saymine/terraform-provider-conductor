package provider

import (
	"context"
	"encoding/json"

	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
)

type scheduleManifestValidator struct{}

func (m scheduleManifestValidator) Description(_ context.Context) string {
	return "Schedule manifest must have a cron expression and a startWorkflowRequest with a workflow name"
}

func (m scheduleManifestValidator) MarkdownDescription(c context.Context) string {
	return m.Description(c)
}

func (m scheduleManifestValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}

	var manifest map[string]interface{}
	if err := json.Unmarshal([]byte(req.ConfigValue.ValueString()), &manifest); err != nil {
		return
	}

	if !hasNonEmptyString(manifest, "cronExpression") && !hasNonEmptyArray(manifest, "cronSchedules") {
		resp.Diagnostics.AddAttributeError(req.Path, "'cronExpression' or a non-empty 'cronSchedules' is required in manifest", "")
	}

	startRequest, ok := manifest["startWorkflowRequest"].(map[string]interface{})
	if !ok {
		resp.Diagnostics.AddAttributeError(req.Path, "'startWorkflowRequest' object is missing from manifest", "")
		return
	}

	if !hasNonEmptyString(startRequest, "name") {
		resp.Diagnostics.AddAttributeError(req.Path, "'startWorkflowRequest.name' must be a non empty string", "")
	}
}

func hasNonEmptyString(m map[string]interface{}, key string) bool {
	value, ok := m[key].(string)
	return ok && value != ""
}

func hasNonEmptyArray(m map[string]interface{}, key string) bool {
	value, ok := m[key].([]interface{})
	return ok && len(value) > 0
}
