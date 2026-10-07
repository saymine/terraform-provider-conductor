package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-jsontypes/jsontypes"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	tfresource "github.com/hashicorp/terraform-plugin-framework/resource"
	tfschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
)

const schedulesPath = "scheduler/schedules"

// Overwritten by the server on every save, so never sent and never compared.
var scheduleServerManagedFields = [5]string{"createTime", "updatedTime", "createdBy", "updatedBy", "nextRunTime"}

var defaultScheduleValues = map[string]interface{}{
	"zoneId": "UTC",
}

var _ tfresource.Resource = &ScheduleResource{}
var _ tfresource.ResourceWithImportState = &ScheduleResource{}
var _ tfresource.ResourceWithModifyPlan = &ScheduleResource{}

type ScheduleResource struct {
	client *conductorHttpClient
}

type ScheduleModel struct {
	Manifest jsontypes.Normalized `tfsdk:"manifest"`
}

func NewScheduleResource() tfresource.Resource {
	return &ScheduleResource{}
}

func (r *ScheduleResource) Metadata(ctx context.Context, req tfresource.MetadataRequest, resp *tfresource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_schedule"
}

func (r *ScheduleResource) Schema(ctx context.Context, req tfresource.SchemaRequest, resp *tfresource.SchemaResponse) {
	resp.Schema = tfschema.Schema{
		Description: "Conductor Workflow Schedule",
		MarkdownDescription: `
Conductor Workflow Schedule - starts a workflow on a cron expression using the Conductor scheduler.
Requires a Conductor server with the scheduler module enabled (` + "`conductor.scheduler.enabled=true`" + `).

The manifest is the JSON body of ` + "`POST /api/scheduler/schedules`" + `: ` + "`name`" + `, ` + "`cronExpression`" + ` (6-field Spring cron, seconds first) or ` + "`cronSchedules`" + `, ` + "`zoneId`" + `, ` + "`startWorkflowRequest`" + `, ` + "`paused`" + `, ` + "`runCatchupScheduleInstances`" + `, ` + "`description`" + `.
Omit ` + "`startWorkflowRequest.version`" + ` to always start the latest workflow version.
`,
		Attributes: map[string]tfschema.Attribute{
			"manifest": tfschema.StringAttribute{
				Description: "The JSON Manifest for the workflow schedule",
				Required:    true,
				CustomType:  jsontypes.NormalizedType{},
				PlanModifiers: []planmodifier.String{
					nameChangedModifier{},
				},
				Validators: []validator.String{
					manifestNameValidator{},
					scheduleManifestValidator{},
				},
			},
		},
	}
}

func (r *ScheduleResource) Configure(ctx context.Context, req tfresource.ConfigureRequest, resp *tfresource.ConfigureResponse) {
	if req.ProviderData == nil { // provider.go Configure hasn't been called yet, so wait longer
		return
	}
	provider, ok := req.ProviderData.(*ConductorProvider)
	if !ok {
		resp.Diagnostics.AddError(
			"Could not create Conductor Provider",
			fmt.Sprintf("Expected *ConductorProvider, got: %T", req.ProviderData),
		)
		return
	}
	r.client = provider.client
}

func (r *ScheduleResource) ModifyPlan(ctx context.Context, req tfresource.ModifyPlanRequest, resp *tfresource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() || req.State.Raw.IsNull() {
		return
	}

	var plan ScheduleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if plan.Manifest.IsNull() || plan.Manifest.IsUnknown() {
		return
	}

	var state ScheduleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if state.Manifest.IsNull() || state.Manifest.IsUnknown() {
		return
	}

	var planDef map[string]interface{}
	if err := json.Unmarshal([]byte(plan.Manifest.ValueString()), &planDef); err != nil {
		return
	}

	var stateDef map[string]interface{}
	if err := json.Unmarshal([]byte(state.Manifest.ValueString()), &stateDef); err != nil {
		return
	}

	cleanupManifestDefaults(ctx, planDef, defaultScheduleValues)
	cleanupManifestDefaults(ctx, stateDef, defaultScheduleValues)

	if reflect.DeepEqual(planDef, stateDef) {
		resp.Diagnostics.Append(resp.Plan.Set(ctx, &state)...)
	}
}

func (r *ScheduleResource) Create(ctx context.Context, req tfresource.CreateRequest, resp *tfresource.CreateResponse) {
	var state ScheduleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	r.save(ctx, state, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update uses the same endpoint as Create: POST /scheduler/schedules is create-or-update keyed by name.
func (r *ScheduleResource) Update(ctx context.Context, req tfresource.UpdateRequest, resp *tfresource.UpdateResponse) {
	var state ScheduleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	r.save(ctx, state, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *ScheduleResource) save(ctx context.Context, state ScheduleModel, diagnostics *diag.Diagnostics) {
	var manifestMap map[string]interface{}
	diagnostics.Append(state.Manifest.Unmarshal(&manifestMap)...)
	if diagnostics.HasError() {
		return
	}

	for _, f := range scheduleServerManagedFields {
		delete(manifestMap, f)
	}

	requestBytes, err := json.Marshal(manifestMap)
	if err != nil {
		diagnostics.AddError("Invalid Manifest", fmt.Sprintf("Manifest Marshal error: %s", err))
		return
	}

	response, err := r.client.do(ctx, http.MethodPost, schedulesPath, bytes.NewBuffer(requestBytes))
	if err != nil {
		diagnostics.AddError("Client Error", fmt.Sprintf("Error sending request: %s", err))
		return
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		body, bodyErr := io.ReadAll(response.Body)
		if bodyErr != nil {
			diagnostics.AddError("HTTP Error", fmt.Sprintf("Received non-OK HTTP status: %s. Failed to read response body: %s",
				response.Status, bodyErr))
			return
		}
		diagnostics.AddError("HTTP Error", fmt.Sprintf("Received non-OK HTTP status: %s. Body: %s", response.Status, string(body)))
		return
	}
}

func (r *ScheduleResource) Read(ctx context.Context, req tfresource.ReadRequest, resp *tfresource.ReadResponse) {
	var state ScheduleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var stateManifestMap map[string]interface{}
	resp.Diagnostics.Append(state.Manifest.Unmarshal(&stateManifestMap)...)
	if resp.Diagnostics.HasError() {
		return
	}

	name := getScheduleNameFromManifest(stateManifestMap, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	requestPath := scheduleByNamePath(name)

	response, err := r.client.do(ctx, http.MethodGet, requestPath, nil)
	if err != nil {
		resp.Diagnostics.AddError("Client Error", fmt.Sprintf("Unable to read schedule, got error: %s", err))
		return
	}
	defer response.Body.Close()

	if response.StatusCode == http.StatusNotFound {
		resp.State.RemoveResource(ctx)
		return
	}

	if response.StatusCode != http.StatusOK {
		resp.Diagnostics.AddError(fmt.Sprintf("HTTP Error path: %s", requestPath), fmt.Sprintf("Received bad HTTP status: %s", response.Status))
		return
	}

	bodyBytes, err := io.ReadAll(response.Body)
	if err != nil {
		resp.Diagnostics.AddError("Error reading response body", err.Error())
		return
	}

	// The server answers 200 with an empty body (serialized null) for an unknown schedule name.
	if isEmptyJsonBody(bodyBytes) {
		resp.State.RemoveResource(ctx)
		return
	}

	var currentManifestMap map[string]interface{}
	if err := json.Unmarshal(bodyBytes, &currentManifestMap); err != nil {
		resp.Diagnostics.AddError("Manifest JSON Parse error", fmt.Sprintf("Manifest must be a valid json: %s", err))
		return
	}

	for _, f := range scheduleServerManagedFields {
		delete(currentManifestMap, f)

		if cValue, ok := stateManifestMap[f]; ok {
			currentManifestMap[f] = cValue
		}
	}

	cleanupManifestDefaults(ctx, currentManifestMap, defaultScheduleValues)
	mergeManifestMaps(ctx, currentManifestMap, stateManifestMap)

	updatedStateBytes, err := json.Marshal(stateManifestMap)
	if err != nil {
		resp.Diagnostics.AddError("Manifest JSON Parse error", fmt.Sprintf("Manifest must be a valid json: %s", err))
		return
	}

	state.Manifest = jsontypes.NewNormalizedValue(string(updatedStateBytes))

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *ScheduleResource) Delete(ctx context.Context, req tfresource.DeleteRequest, resp *tfresource.DeleteResponse) {
	var state ScheduleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var manifestMap map[string]interface{}
	resp.Diagnostics.Append(state.Manifest.Unmarshal(&manifestMap)...)
	if resp.Diagnostics.HasError() {
		return
	}

	name := getScheduleNameFromManifest(manifestMap, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	response, err := r.client.do(ctx, http.MethodDelete, scheduleByNamePath(name), nil)
	if err != nil {
		resp.Diagnostics.AddError("Delete Error", fmt.Sprintf("Unable to delete schedule, got error: %s", err))
		return
	}
	defer response.Body.Close()

	switch response.StatusCode {
	case http.StatusOK, http.StatusNoContent, http.StatusNotFound:
		return
	}

	bodyBytes, err := io.ReadAll(response.Body)
	var bodyStr string
	if err == nil {
		bodyStr = string(bodyBytes)
	} else {
		bodyStr = fmt.Sprintf("Read All Body Error: %s", err)
	}
	resp.Diagnostics.AddError("HTTP Error", fmt.Sprintf("Received non-OK HTTP status: %s. Body: %s", response.Status, bodyStr))
}

func (r *ScheduleResource) ImportState(ctx context.Context, req tfresource.ImportStateRequest, resp *tfresource.ImportStateResponse) {
	initialStateMap := map[string]interface{}{
		"name": req.ID,
	}

	manifestBytes, err := json.Marshal(initialStateMap)
	if err != nil {
		resp.Diagnostics.AddError("Invalid ID", fmt.Sprintf("Manifest Marshal error: %s", err))
		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("manifest"), string(manifestBytes))...)
}

func scheduleByNamePath(name string) string {
	return fmt.Sprintf("%s/%s", schedulesPath, url.PathEscape(name))
}

func isEmptyJsonBody(body []byte) bool {
	trimmed := strings.TrimSpace(string(body))
	return trimmed == "" || trimmed == "null"
}

func getScheduleNameFromManifest(manifestMap map[string]interface{}, diagnostics *diag.Diagnostics) string {
	nameVal, ok := manifestMap["name"]
	if !ok {
		diagnostics.AddError("Invalid Manifest", "'name' parameter is missing from manifest")
		return ""
	}

	name, ok := nameVal.(string)
	if !ok || name == "" {
		diagnostics.AddError("Invalid Manifest", "'name' parameter must be string")
		return ""
	}

	return name
}
