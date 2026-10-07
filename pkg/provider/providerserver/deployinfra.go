package providerserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"

	connect "connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	"github.com/ocelhq/ocel/pkg/progress"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

func (h *handlers) ProvisionInfra(ctx context.Context, req *contractv1.ProvisionInfraRequest, stream *connect.ServerStream[progressv1.OperationEvent]) error {
	return streamResult(ctx, stream, func(sender *eventStream) (*progressv1.OperationEvent, error) {
		if err := refuseInfraRequest(req); err != nil {
			return nil, err
		}
		run, err := h.openDeploy(ctx, &contractv1.DeployRequest{
			Manifest:       req.GetManifest(),
			Environment:    req.GetEnvironment(),
			Edge:           req.GetEdge(),
			InlineBindings: req.GetInlineBindings(),
		}, sender)
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
	return nil
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
	if err := r.provisionInfra(ctx); err != nil {
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
	if err := r.readInlineRecords(ctx); err != nil {
		return err
	}
	return r.admitBindings(ctx, progress)
}

func (r *deployRun) refuseUnprovisionedInfra(ctx context.Context) error {
	if !r.infraProvisioned {
		return nil
	}
	recorded, found, err := stackrecords.Read(ctx, r.provider.KeyValues(), r.spec.Tier, r.spec.Slug, r.spec.Infra)
	if err != nil {
		return err
	}
	declared, err := readResourceDigest(r.manifest)
	if err != nil {
		return err
	}
	if !found || recorded.ResourceDigest != declared {
		return refusal.Refuse(refusal.CodeBusy,
			"%s holds other resources than this deploy declares, so another deploy provisioned it after this one did: deploy again once that deploy ends",
			r.spec.Infra)
	}
	return nil
}

func readResourceDigest(manifest *contractv1.Manifest) (string, error) {
	encoded, err := proto.MarshalOptions{Deterministic: true}.Marshal(&contractv1.Manifest{Resources: manifest.GetResources()})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}
