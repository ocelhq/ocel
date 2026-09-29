package providerserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"time"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

func forwardsToRouter(front edge.Edge) bool {
	facts := front.Facts()
	return facts.ProxiesRecords && !facts.RunsCode
}

type shieldedClaim func(ctx context.Context, clientCertificates []string) (edge.Origin, error)

func claimShielded(ctx context.Context, front edge.Edge, hostname string, claim shieldedClaim) (edge.Origin, []string, error) {
	certificates := front.Hooks().ClientCertificates
	if certificates == nil {
		origin, err := claim(ctx, nil)
		return origin, nil, err
	}
	staged, err := certificates.Ensure(ctx, hostname)
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
	presented, err := certificates.Ensure(ctx, hostname)
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
	staged, err := certificates.Ensure(ctx, hostname)
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

type originClaim struct {
	origin  *edge.Origin
	trusted []string
	issued  edge.OriginCertificate
}

func (c originClaim) recordIssued(hostState *stackrecords.HostnameState) string {
	if c.issued.ID == "" {
		return ""
	}
	superseded := hostState.OriginCertificate
	hostState.OriginCertificate, hostState.OriginCertificateExpires = c.issued.ID, c.issued.ExpiresAt
	if superseded == c.issued.ID {
		return ""
	}
	return superseded
}

func claimCertified(ctx context.Context, front edge.Edge, claim router.Claim, issued *edge.OriginCertificate,
	take func(context.Context, router.Claim) (edge.Origin, error),
) (edge.Origin, error) {
	origin, err := take(ctx, claim)
	certificates := front.Hooks().OriginCertificates
	if err != nil || origin.Address == "" || origin.Certified || certificates == nil {
		return origin, err
	}
	certificate, err := certificates.Issue(ctx, claim.Hostname)
	if err != nil {
		return edge.Origin{}, err
	}
	*issued = certificate
	claim.OriginCertificate = certificate
	return take(ctx, claim)
}

func originCertificateDue(hostState *stackrecords.HostnameState, now time.Time) bool {
	return hostState.OriginCertificate != "" &&
		edge.OriginCertificate{ExpiresAt: hostState.OriginCertificateExpires}.IsDue(now)
}

func revokeOriginCertificate(ctx context.Context, front edge.Edge, id string, runProgress progress.Progress) {
	certificates := front.Hooks().OriginCertificates
	if id == "" || certificates == nil {
		return
	}
	if err := certificates.Revoke(ctx, id); err != nil {
		runProgress.Warn(fmt.Sprintf("the origin certificate %s is no longer answered with, and stays valid until it expires: revoking it failed: %v", id, err))
	}
}
