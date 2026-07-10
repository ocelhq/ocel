package main

import (
	"context"
	"fmt"

	connect "connectrpc.com/connect"

	providerv1 "github.com/ocelhq/ocel/pkg/proto/provider/v1"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
)

// Server implements providerv1connect.ProviderServiceHandler. Deploy is
// stubbed: it validates that a well-formed manifest was received, emits a
// couple of progress events, and always reports terminal success. No real
// provisioning against AWS happens yet.
type Server struct{}

func (s *Server) Deploy(_ context.Context, req *providerv1.DeployRequest, stream *connect.ServerStream[providerv1.DeployEvent]) error {
	manifest := req.GetManifest()
	if err := validateManifest(manifest); err != nil {
		return connect.NewError(connect.CodeInvalidArgument, err)
	}

	events := []*providerv1.DeployEvent{
		progressEvent("validated manifest"),
		progressEvent(fmt.Sprintf("stubbed provisioning of %d resource(s)", len(manifest.GetResources()))),
		resultEvent(true, ""),
	}
	for _, event := range events {
		if err := stream.Send(event); err != nil {
			return err
		}
	}
	return nil
}

func progressEvent(message string) *providerv1.DeployEvent {
	return &providerv1.DeployEvent{
		Event: &providerv1.DeployEvent_Progress{Progress: &providerv1.ProgressEvent{Message: message}},
	}
}

func resultEvent(success bool, errMsg string) *providerv1.DeployEvent {
	return &providerv1.DeployEvent{
		Event: &providerv1.DeployEvent_Result{Result: &providerv1.ResultEvent{Success: success, Error: errMsg}},
	}
}

// validateManifest reports whether manifest is well-formed enough for the
// (stubbed) provider to act on: a schema version and project id are set, and
// every resource entry carries a logical name, a typed resource identifier,
// and a typed config. It does not check the manifest against a specific
// schema_version value — that's a provider-implementation decision for when
// real provisioning lands.
func validateManifest(m *providerv1.Manifest) error {
	if m == nil {
		return fmt.Errorf("manifest is required")
	}
	if m.GetSchemaVersion() == "" {
		return fmt.Errorf("manifest.schema_version is required")
	}
	if m.GetProjectId() == "" {
		return fmt.Errorf("manifest.project_id is required")
	}
	for i, r := range m.GetResources() {
		if r.GetLogicalName() == "" {
			return fmt.Errorf("manifest.resources[%d]: logical_name is required", i)
		}
		if r.GetResource() == nil || r.GetResource().GetType() == resourcesv1.ResourceType_RESOURCE_TYPE_UNSPECIFIED {
			return fmt.Errorf("manifest.resources[%d] (%s): a valid resource type is required", i, r.GetLogicalName())
		}
		if r.GetConfig() == nil {
			return fmt.Errorf("manifest.resources[%d] (%s): typed config is required", i, r.GetLogicalName())
		}
	}
	return nil
}
