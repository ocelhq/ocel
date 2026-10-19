package providerserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/ocelhq/ocel/pkg/edge"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

type infraSpecInput struct {
	Resources  []byte             `json:"resources"`
	Tags       map[string]string  `json:"tags"`
	Edge       edge.Kind          `json:"edge"`
	Parts      []string           `json:"parts"`
	Options    provider.Options   `json:"options"`
	Transforms []string           `json:"transforms"`
	WrittenBy  provider.WrittenBy `json:"writtenBy"`
}

func (r *deployRun) digestInfraSpec() (string, error) {
	declared, err := encodeResources(r.manifest.GetResources())
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(infraSpecInput{
		Resources:  declared,
		Tags:       infraTags(r.spec),
		Edge:       r.front.Kind(),
		Parts:      r.parts,
		Options:    r.options,
		Transforms: r.transforms,
		WrittenBy:  r.readiness.WrittenBy,
	})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

type recordedInfra struct {
	stack      stackrecords.Stack
	found      bool
	digest     string
	undeclared []*contractv1.ManifestResource
}

func (r *deployRun) readRecordedInfra(ctx context.Context) (recordedInfra, error) {
	stack, found, err := stackrecords.Read(ctx, r.provider.KeyValues(), r.spec.Tier, r.spec.Slug, r.spec.Infra)
	if err != nil {
		return recordedInfra{}, err
	}
	digest, err := r.digestInfraSpec()
	if err != nil {
		return recordedInfra{}, err
	}
	undeclared, err := r.listUndeclaredResources(stack)
	if err != nil {
		return recordedInfra{}, err
	}
	return recordedInfra{stack: stack, found: found, digest: digest, undeclared: undeclared}, nil
}

func (r *deployRun) readCurrentInfra(ctx context.Context) ([]*contractv1.ManifestResource, error) {
	infra, err := r.readRecordedInfra(ctx)
	if err != nil {
		return nil, err
	}
	if err := r.refuseUnprovisionedInfra(infra); err != nil {
		return nil, err
	}
	if !r.isInfraCurrent(infra) {
		return infra.undeclared, nil
	}
	published, err := r.publishedBindings().Published(ctx)
	if err != nil {
		return nil, err
	}
	bindings, err := mergePublishedProperties(r.spec.Infra, infra.stack.Bindings, published)
	if err != nil && r.infraProvisioned {
		return nil, err
	}
	r.bindings, r.infraCurrent = bindings, err == nil
	return infra.undeclared, nil
}

func (r *deployRun) refuseUnprovisionedInfra(infra recordedInfra) error {
	if !r.infraProvisioned {
		return nil
	}
	if !infra.found {
		return refusal.Refuse(refusal.CodeNotReady,
			"%s was never provisioned, so this deploy has no infra to ship its apps over: deploy again, and the infra is provisioned before the build",
			r.spec.Infra)
	}
	if infra.stack.SpecDigest != infra.digest {
		return refusal.Refuse(refusal.CodeBusy,
			"%s was last provisioned from another spec than this deploy's, so another deploy provisioned it after this one did: deploy again once that deploy ends",
			r.spec.Infra)
	}
	return nil
}

func (r *deployRun) isInfraCurrent(infra recordedInfra) bool {
	if len(infra.undeclared) > 0 {
		return false
	}
	if len(r.manifest.GetResources()) == 0 && (!infra.found || infra.stack.SpecDigest != "") {
		return true
	}
	return infra.found && infra.stack.SpecDigest == infra.digest && (r.infraProvisioned || len(r.transforms) == 0)
}
