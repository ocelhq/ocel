package providerserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"slices"

	"github.com/ocelhq/ocel/pkg/edge"
)

type shieldedClaim func(ctx context.Context, clientCertificates []string) (edge.Origin, error)

func claimShielded(ctx context.Context, front edge.Edge, hostname string, claim shieldedClaim) (edge.Origin, []string, error) {
	certificates := front.Hooks().ClientCertificates
	if certificates == nil {
		origin, err := claim(ctx, nil)
		return origin, nil, err
	}
	staged, err := certificates.Stage(ctx, hostname)
	if err != nil {
		return edge.Origin{}, nil, err
	}
	origin, err := claim(ctx, staged)
	if err != nil {
		return edge.Origin{}, nil, err
	}
	if err := certificates.Present(ctx, hostname); err != nil {
		return edge.Origin{}, nil, err
	}
	presented, err := certificates.Stage(ctx, hostname)
	if err != nil {
		return edge.Origin{}, nil, err
	}
	if slices.Equal(presented, staged) {
		return origin, staged, nil
	}
	origin, err = claim(ctx, presented)
	return origin, presented, err
}

func stagedClientCertificatesChanged(ctx context.Context, front edge.Edge, hostname string, trusted []string) (bool, error) {
	certificates := front.Hooks().ClientCertificates
	if certificates == nil {
		return false, nil
	}
	staged, err := certificates.Stage(ctx, hostname)
	if err != nil {
		return false, err
	}
	return !slices.Equal(digestClientCertificates(staged), trusted), nil
}

func digestClientCertificates(certificates []string) []string {
	digests := make([]string, 0, len(certificates))
	for _, certificate := range certificates {
		sum := sha256.Sum256([]byte(certificate))
		digests = append(digests, hex.EncodeToString(sum[:]))
	}
	slices.Sort(digests)
	return slices.Compact(digests)
}
