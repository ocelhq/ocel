package providerserver

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"

	connect "connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

func (h *handlers) ProvisionInfra(ctx context.Context, req *contractv1.ProvisionInfraRequest, stream *connect.ServerStream[progressv1.OperationEvent]) error {
	return streamResult(ctx, stream, func(sender *eventStream) (*progressv1.OperationEvent, error) {
		if err := refuseInfraRequest(req); err != nil {
			return nil, err
		}
		spec, err := newInfraSpec(req)
		if err != nil {
			return nil, err
		}
		hold, err := h.takeEnvironment(ctx, spec, req.GetLeaseToken())
		if err != nil {
			return nil, err
		}
		result, err := h.executeInfraRun(hold.context(ctx), req, spec, sender)
		if err != nil && !hold.held {
			_ = hold.release(ctx)
		}
		hold.end()
		return result, hold.explain(err)
	})
}

func (h *handlers) executeInfraRun(ctx context.Context, req *contractv1.ProvisionInfraRequest, spec provider.DeploySpec, sender *eventStream) (*progressv1.OperationEvent, error) {
	deploy := &contractv1.DeployRequest{
		Manifest:       req.GetManifest(),
		Environment:    req.GetEnvironment(),
		Edge:           req.GetEdge(),
		InlineBindings: req.GetInlineBindings(),
		AliasToken:     req.GetAliasToken(),
		Consented:      req.GetConsented(),
	}
	mode, err := deployModeOf(deploy, 0)
	if err != nil {
		return nil, err
	}
	run, err := h.openDeploy(ctx, deploy, spec, mode, sender)
	if err != nil {
		return nil, err
	}
	return run.executeInfra(ctx)
}

func refuseInfraRequest(req *contractv1.ProvisionInfraRequest) error {
	if len(req.GetManifest().GetApps()) > 0 {
		return refusal.Refuse(refusal.CodeInvalid,
			"this manifest declares apps, and ProvisionInfra provisions the infra stack alone: the apps ship through Deploy once they are built")
	}
	return refuseInvalidWorkers(req.GetManifest())
}

func newInfraSpec(req *contractv1.ProvisionInfraRequest) (provider.DeploySpec, error) {
	return newDeploySpec(&contractv1.DeployRequest{
		Manifest:    &contractv1.Manifest{Slug: req.GetManifest().GetSlug()},
		Environment: req.GetEnvironment(),
	})
}

func refuseInfraProvisionedDeploy(req *contractv1.DeployRequest) error {
	if !req.GetInfraProvisioned() {
		return nil
	}
	if req.GetDry() {
		return refusal.Refuse(refusal.CodeInvalid,
			"a dry deploy plans its infra stack with its apps, so it cannot also say its infra was provisioned already")
	}
	if req.GetLeaseToken() == "" {
		return refusal.Refuse(refusal.CodeInvalid,
			"this deploy says its infra was provisioned but sends no lease token, and only the deploy whose lease token provisioned that infra may deploy over it")
	}
	return nil
}

func (r *deployRun) executeInfra(ctx context.Context) (*progressv1.OperationEvent, error) {
	undeclared, err := r.readCurrentInfra(ctx)
	if err != nil {
		return nil, err
	}
	if err := r.spanEvents.run(r.spans.Environment, func(env *spanRun) error {
		return env.phase(func(progress progress.Log) error {
			return r.prepareInfra(ctx, progress, undeclared)
		})
	}); err != nil {
		return nil, err
	}
	if err := r.reconcileInfra(ctx, undeclared); err != nil {
		return nil, err
	}
	return okResult(), nil
}

func (r *deployRun) prepareInfra(ctx context.Context, progress progress.Log, undeclared []*contractv1.ManifestResource) error {
	if err := r.refuseOtherLifecycle(ctx); err != nil {
		return err
	}
	if err := r.checkInlineBindings(ctx, progress); err != nil {
		return err
	}
	if err := r.refuseUnreadyBootstrap(ctx, progress); err != nil {
		return err
	}
	if err := r.resolveServingDomains(ctx); err != nil {
		return err
	}
	if err := r.readInlineRecords(ctx); err != nil {
		return err
	}
	if err := r.admitBindings(ctx, progress); err != nil {
		return err
	}
	if err := r.refuseUnsupportedDeclarations(ctx); err != nil {
		return err
	}
	if err := r.refuseUnconsentedInfra(ctx, undeclared); err != nil {
		return err
	}
	if err := r.ensureProject(ctx); err != nil {
		return err
	}
	return r.ensurePreviewRecorded(ctx)
}

func (r *deployRun) ensureProject(ctx context.Context) error {
	return records.Change(ctx, r.provider.KeyValues(), stackrecords.ProjectKey(r.spec.Tier, r.spec.Slug), func(entry records.Entry) ([]byte, bool, error) {
		if len(entry.Value) > 0 {
			return nil, false, nil
		}
		value, err := json.Marshal(stackrecords.Project{Parts: r.parts})
		return value, true, err
	})
}

func (r *deployRun) ensurePreviewRecorded(ctx context.Context) error {
	if r.spec.Tier != environment.TierPreview {
		return nil
	}
	alias, err := r.ensureBuiltAlias(ctx)
	if err != nil {
		return err
	}
	r.aliasToken = alias
	return r.ensureLifecycle(ctx)
}

func mergePublishedProperties(infra naming.StackName, recorded, published []provider.Binding) ([]provider.Binding, error) {
	bindings := slices.Clone(recorded)
	for i, binding := range bindings {
		at := slices.IndexFunc(published, func(p provider.Binding) bool { return p.Name == binding.Name && p.Type == binding.Type })
		if at < 0 {
			return nil, refusal.Refuse(refusal.CodeNotReady,
				"%s records %s binding %s, and no %s binding named %s is published any more, so this deploy has none of the secrets the record never keeps to grant its apps: "+
					"deploy again, and the infra is provisioned and published before the build",
				infra, binding.Type, binding.Name, binding.Type, binding.Name)
		}
		bindings[i].Properties = map[string]string{}
		maps.Copy(bindings[i].Properties, binding.Properties)
		maps.Copy(bindings[i].Properties, published[at].Properties)
	}
	return bindings, nil
}

func (r *deployRun) recordInfraStack(ctx context.Context, holds []byte) error {
	recorded, found, err := stackrecords.Read(ctx, r.provider.KeyValues(), r.spec.Tier, r.spec.Slug, r.spec.Infra)
	if err != nil {
		return err
	}
	if !found {
		recorded = stackrecords.Stack{Kind: provider.StackInfra, WrittenBy: r.readiness.WrittenBy}
	}
	recorded.Resources, recorded.SpecDigest = holds, ""
	return stackrecords.Write(ctx, r.provider.KeyValues(), r.spec.Tier, r.spec.Slug, r.spec.Infra, recorded)
}

func (r *deployRun) listUndeclaredResources(recorded stackrecords.Stack) ([]*contractv1.ManifestResource, error) {
	var held contractv1.Manifest
	if err := proto.Unmarshal(recorded.Resources, &held); err != nil {
		return nil, fmt.Errorf("read the resources %s holds: %w", r.spec.Infra, err)
	}
	declared := map[typedResourceName]*contractv1.ManifestResource{}
	for _, resource := range r.manifest.GetResources() {
		declared[newTypedResourceName(resource)] = resource
	}
	return slices.DeleteFunc(held.GetResources(), func(resource *contractv1.ManifestResource) bool {
		declaration, found := declared[newTypedResourceName(resource)]
		return found && !isNewlyBound(resource, declaration)
	}), nil
}

func isNewlyBound(recorded, declared *contractv1.ManifestResource) bool {
	return recorded.GetBinding() == "" && declared.GetBinding() != ""
}

type typedResourceName struct {
	resourceType resourcesv1.ResourceType
	name         string
}

func newTypedResourceName(resource *contractv1.ManifestResource) typedResourceName {
	return typedResourceName{resource.GetResource().GetType(), resourceName(resource)}
}

func resourceName(resource *contractv1.ManifestResource) string {
	return cmp.Or(resource.GetLogicalName(), resource.GetResource().GetName())
}

func encodeResources(resources []*contractv1.ManifestResource) ([]byte, error) {
	return proto.MarshalOptions{Deterministic: true}.Marshal(&contractv1.Manifest{Resources: resources})
}
