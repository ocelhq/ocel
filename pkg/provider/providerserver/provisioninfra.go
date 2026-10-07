package providerserver

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"

	connect "connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/progress"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
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
		run, err := h.openDeploy(ctx, &contractv1.DeployRequest{
			Manifest:       req.GetManifest(),
			Environment:    req.GetEnvironment(),
			Edge:           req.GetEdge(),
			InlineBindings: req.GetInlineBindings(),
			AliasToken:     req.GetAliasToken(),
		}, spec, sender)
		if err != nil {
			return nil, err
		}
		return run.executeInfra(ctx)
	})
}

func refuseInfraRequest(req *contractv1.ProvisionInfraRequest) error {
	if len(req.GetManifest().GetApps()) > 0 {
		return refusal.Refuse(refusal.CodeInvalid,
			"this manifest declares apps, and ProvisionInfra provisions the infra stack alone: the apps ship through Deploy once they are built")
	}
	if ephemeral(req.GetEnvironment()) {
		return refusal.Refuse(refusal.CodeInvalid,
			"an ephemeral preview has no infra stack of its own, only the bindings its tier publishes, so there is no infra to provision before its build")
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
	if ephemeral(req.GetEnvironment()) {
		return refusal.Refuse(refusal.CodeInvalid,
			"an ephemeral preview has no infra stack of its own, so no infra was provisioned for this deploy")
	}
	return nil
}

func (r *deployRun) executeInfra(ctx context.Context) (*progressv1.OperationEvent, error) {
	if err := r.spanEvents.run(r.spans.Environment, func(env *spanRun) error {
		return env.phase(func(progress progress.Log) error {
			return r.prepareInfra(ctx, progress)
		})
	}); err != nil {
		return nil, err
	}
	recorded, _, err := stackrecords.Read(ctx, r.provider.KeyValues(), r.spec.Tier, r.spec.Slug, r.spec.Infra)
	if err != nil {
		return nil, err
	}
	undeclared, err := r.listUndeclaredResources(recorded)
	if err != nil {
		return nil, err
	}
	if err := r.provisionInfra(ctx, undeclared); err != nil {
		return nil, err
	}
	return okResult(), nil
}

func (r *deployRun) prepareInfra(ctx context.Context, progress progress.Log) error {
	if err := r.refuseOtherLifecycle(ctx); err != nil {
		return err
	}
	if err := r.checkInlineBindings(ctx, progress); err != nil {
		return err
	}
	if err := r.ensureBootstrap(ctx, progress); err != nil {
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
	if err := r.ensureProject(ctx); err != nil {
		return err
	}
	return r.ensurePreviewRecorded(ctx)
}

func (r *deployRun) ensureProject(ctx context.Context) error {
	name := stackrecords.ProjectKey(r.spec.Tier, r.spec.Slug)
	recorded, err := keyvalue.ReadOrEmpty(ctx, r.provider.KeyValues(), name)
	if err != nil {
		return fmt.Errorf("read %s: %w", name, err)
	}
	if len(recorded.Value) > 0 {
		return nil
	}
	if recorded.Value, err = json.Marshal(stackrecords.Project{Features: r.features}); err != nil {
		return fmt.Errorf("record %s: %w", name, err)
	}
	if _, err := r.provider.KeyValues().Write(ctx, recorded); err != nil {
		return fmt.Errorf("record %s: %w", name, err)
	}
	return nil
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

func (r *deployRun) readProvisionedInfra(ctx context.Context) error {
	if !r.infraProvisioned {
		return nil
	}
	recorded, found, err := stackrecords.Read(ctx, r.provider.KeyValues(), r.spec.Tier, r.spec.Slug, r.spec.Infra)
	if err != nil {
		return err
	}
	if !found {
		return refusal.Refuse(refusal.CodeNotReady,
			"%s was never provisioned, so this deploy has no infra to ship its apps over: deploy again, and the infra is provisioned before the build",
			r.spec.Infra)
	}
	declared, err := encodeResources(r.manifest.GetResources())
	if err != nil {
		return err
	}
	if recorded.ResourceDigest != digestResources(declared) {
		return refusal.Refuse(refusal.CodeBusy,
			"%s holds other resources than this deploy declares, so another deploy provisioned it after this one did: deploy again once that deploy ends",
			r.spec.Infra)
	}
	undeclared, err := r.listUndeclaredResources(recorded)
	if err != nil {
		return err
	}
	r.infraHoldsUndeclared = len(undeclared) > 0
	return nil
}

func (r *deployRun) listUndeclaredResources(recorded stackrecords.Stack) ([]*contractv1.ManifestResource, error) {
	var held contractv1.Manifest
	if err := proto.Unmarshal(recorded.Resources, &held); err != nil {
		return nil, fmt.Errorf("read the resources %s holds: %w", r.spec.Infra, err)
	}
	declared := map[string]bool{}
	for _, resource := range r.manifest.GetResources() {
		declared[resourceName(resource)] = true
	}
	return slices.DeleteFunc(held.GetResources(), func(resource *contractv1.ManifestResource) bool {
		return declared[resourceName(resource)]
	}), nil
}

func resourceName(resource *contractv1.ManifestResource) string {
	return cmp.Or(resource.GetLogicalName(), resource.GetResource().GetName())
}

func encodeResources(resources []*contractv1.ManifestResource) ([]byte, error) {
	return proto.MarshalOptions{Deterministic: true}.Marshal(&contractv1.Manifest{Resources: resources})
}

func digestResources(encoded []byte) string {
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}
