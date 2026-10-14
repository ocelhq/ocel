package cloudfront

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudfrontkeyvaluestore"
	kvstypes "github.com/aws/aws-sdk-go-v2/service/cloudfrontkeyvaluestore/types"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/edge/gate"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/cloudfront/resolver"
)

const (
	gatePrefix = "gate/"

	keyValueStoreValueMaxBytes = 1024
)

func gateKey(slug string) string { return gatePrefix + slug }

func revokeCommand(tier environment.Tier) string {
	if tier == environment.TierPreview {
		return "ocel protection bypass revoke --preview"
	}
	return "ocel protection bypass revoke"
}

func (p *cloudFront) publishGate(ctx context.Context, publication edge.GatePublication) error {
	s := &stack{p: p, state: publication.State}
	if err := publication.State.Private.Into(&s.own); err != nil {
		return err
	}
	c, err := p.clientsFor(ctx)
	if err != nil {
		return err
	}
	if publication.Gate != nil {
		deployed, err := p.bootstrap(ctx, c, publication.State.Tier)
		if err != nil {
			return err
		}
		if err := refuseResolverWithoutGate(deployed, publication.State.Tier, publication.State.Slug); err != nil {
			return err
		}
	}
	return s.reconcileGate(ctx, c, publication.Gate)
}

func refuseResolverWithoutGate(deployed bootstrap.Deployed, tier environment.Tier, slug string) error {
	if deployed.Outputs[bootstrap.OutputEdgeGateDigest] == resolver.GateDigest() {
		return nil
	}
	return refusal.Refuse(refusal.CodeNotReady,
		"protection.%s is on for %s, and the resolver function of this account's %s bootstrap does not run the gate this CLI publishes, so it would serve the %s tier to anyone: run `%s` to install the resolver that runs it",
		tier, slug, tier, tier, provider.BootstrapCommand(tier))
}

func (s *stack) reconcileGate(ctx context.Context, c Clients, gate *gate.Gate) error {
	want, err := encodeGate(s.slug(), s.tier(), gate)
	if err != nil {
		return err
	}
	routes := s.routes(c)
	key := gateKey(s.slug())
	have, recorded, err := routes.read(ctx, key)
	if err != nil {
		return err
	}
	switch {
	case gate == nil && !recorded:
		return nil
	case gate == nil:
		return routes.write(ctx, nil, []kvstypes.DeleteKeyRequestListItem{{Key: aws.String(key)}})
	case recorded && have == want:
		return nil
	}
	return routes.write(ctx, []kvstypes.PutKeyRequestListItem{{Key: aws.String(key), Value: aws.String(want)}}, nil)
}

func encodeGate(slug string, tier environment.Tier, gate *gate.Gate) (string, error) {
	if gate == nil {
		return "", nil
	}
	encoded, err := json.Marshal(gate)
	if err != nil {
		return "", fmt.Errorf("encode the keys that gate the %s tier of %s: %w", tier, slug, err)
	}
	if len(encoded) > keyValueStoreValueMaxBytes {
		return "", refusal.Refuse(refusal.CodeInvalid,
			"the keys that gate the %s tier of %s take %d bytes, and the %q edge keeps them in a key value store that holds %d per value. Revoke bypass secrets with `%s` until they fit",
			tier, slug, len(encoded), Kind, keyValueStoreValueMaxBytes, revokeCommand(tier))
	}
	return string(encoded), nil
}

func (w routeStore) read(ctx context.Context, key string) (string, bool, error) {
	if w.arn == "" {
		return "", false, fmt.Errorf("the %q edge names no key value store to read %s from; bootstrap the account first", Kind, key)
	}
	out, err := w.clients.KeyValueStore.GetKey(ctx, &cloudfrontkeyvaluestore.GetKeyInput{
		KvsARN: aws.String(w.arn),
		Key:    aws.String(key),
	})
	if err != nil {
		if missingRoute(err) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("read %s from the key value store the %q edge routes with: %w", key, Kind, err)
	}
	return aws.ToString(out.Value), true, nil
}
