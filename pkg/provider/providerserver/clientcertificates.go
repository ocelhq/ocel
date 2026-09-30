package providerserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

type originTake func(ctx context.Context, claim router.Claim) (edge.Origin, error)

type originReservation func(ctx context.Context, issued edge.OriginCertificate) (release func(context.Context) error, err error)

type originClaim struct {
	origin  *edge.Origin
	trusted []string
	issued  edge.OriginCertificate
}

func (c originClaim) isTunneled() bool { return c.origin != nil && c.origin.Tunneled }

func (c originClaim) recordOn(hostState *stackrecords.HostnameState) (superseded string) {
	hostState.Tunneled = c.isTunneled()
	if hostState.Tunneled {
		superseded = hostState.OriginCertificateID
		hostState.ClientCertificateDigests = nil
		hostState.OriginCertificateID, hostState.OriginCertificateExpiresAt = "", time.Time{}
		return superseded
	}
	hostState.ClientCertificateDigests = digestClientCertificates(c.trusted)
	if c.issued.ID == "" {
		return ""
	}
	superseded = hostState.OriginCertificateID
	hostState.OriginCertificateID, hostState.OriginCertificateExpiresAt = c.issued.ID, c.issued.ExpiresAt
	if superseded == c.issued.ID {
		return ""
	}
	return superseded
}

func claimOrigin(ctx context.Context, front edge.Edge, claim router.Claim, take originTake, reserve originReservation) (originClaim, error) {
	if claim.Tunnel != edge.None {
		return claimTunneled(ctx, claim, take)
	}
	var claimed originClaim
	origin, trusted, err := claimTrusting(ctx, front, claim.Hostname, func(ctx context.Context, clientCertificates []string) (edge.Origin, error) {
		claim.ClientCertificates = clientCertificates
		origin, issued, err := claimCertified(ctx, front, claim, take, reserve)
		if issued.ID != "" {
			claimed.issued = issued
		}
		return origin, err
	})
	claimed.trusted = trusted
	if err != nil || origin.Address == "" {
		return claimed, err
	}
	claimed.origin = &origin
	return claimed, nil
}

func claimTunneled(ctx context.Context, claim router.Claim, take originTake) (originClaim, error) {
	origin, err := take(ctx, claim)
	if err != nil {
		return originClaim{}, err
	}
	if !origin.Tunneled {
		return originClaim{}, fmt.Errorf("%s was claimed through the %s edge's tunnel, and the router answered with %q, which is no tunnel", claim.Hostname, claim.Tunnel, origin.Address)
	}
	return originClaim{origin: &origin}, nil
}

func readOriginClaimStaleness(ctx context.Context, front edge.Edge, hostname string, tunnel edge.Kind, hostState *stackrecords.HostnameState, now time.Time) (why string, err error) {
	wanted := tunnel != edge.None
	switch {
	case wanted && !hostState.Tunneled:
		return "it is reached through a tunnel now", nil
	case !wanted && hostState.Tunneled:
		return "it is no longer reached through a tunnel", nil
	case wanted:
		return "", nil
	}
	changed, err := clientCertificatesChanged(ctx, front, hostname, hostState.ClientCertificateDigests)
	if err != nil {
		return "", err
	}
	if isOriginCertificateDue(hostState, now) {
		return "the certificate its origin answers it with is due for renewal", nil
	}
	if changed {
		return fmt.Sprintf("the client certificates %s presents to its origin changed", describeFront(front.Kind())), nil
	}
	return "", nil
}

func claimTrusting(ctx context.Context, front edge.Edge, hostname string, claim func(context.Context, []string) (edge.Origin, error)) (edge.Origin, []string, error) {
	certificates := front.Hooks().ClientCertificates
	if certificates == nil {
		origin, err := claim(ctx, nil)
		return origin, nil, err
	}
	trusted, err := certificates.Ensure(ctx, hostname)
	if err != nil {
		return edge.Origin{}, nil, err
	}
	origin, err := claim(ctx, trusted)
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
	if slices.Equal(presented, trusted) {
		return origin, trusted, nil
	}
	origin, err = claim(ctx, presented)
	return origin, presented, err
}

func claimCertified(ctx context.Context, front edge.Edge, claim router.Claim, take originTake, reserve originReservation) (edge.Origin, edge.OriginCertificate, error) {
	origin, err := take(ctx, claim)
	certificates := front.Hooks().OriginCertificates
	if err != nil || origin.Address == "" || origin.Certified || certificates == nil {
		return origin, edge.OriginCertificate{}, err
	}
	issued, err := certificates.Issue(ctx, claim.Hostname)
	if err != nil {
		return edge.Origin{}, edge.OriginCertificate{}, err
	}
	release := func(context.Context) error { return nil }
	if reserve != nil {
		if release, err = reserve(ctx, issued); err != nil {
			return edge.Origin{}, edge.OriginCertificate{}, errors.Join(err, revokeUnused(ctx, certificates, issued.ID))
		}
	}
	claim.OriginCertificate = issued
	if origin, err = take(ctx, claim); err != nil {
		return edge.Origin{}, edge.OriginCertificate{}, errors.Join(err, revokeUnused(ctx, certificates, issued.ID), release(ctx))
	}
	return origin, issued, nil
}

func revokeUnused(ctx context.Context, certificates *edge.OriginCertificateHooks, id string) error {
	if err := certificates.Revoke(ctx, id); err != nil {
		return fmt.Errorf("the origin certificate %s answers nothing and stays valid until it expires: revoking it failed: %w", id, err)
	}
	return nil
}

func clientCertificatesChanged(ctx context.Context, front edge.Edge, hostname string, trusted []string) (bool, error) {
	certificates := front.Hooks().ClientCertificates
	if certificates == nil {
		return false, nil
	}
	held, err := certificates.Ensure(ctx, hostname)
	if err != nil {
		return false, err
	}
	return !slices.Equal(digestClientCertificates(held), trusted), nil
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

func isOriginCertificateDue(hostState *stackrecords.HostnameState, now time.Time) bool {
	return hostState.OriginCertificateID != "" &&
		edge.OriginCertificate{ExpiresAt: hostState.OriginCertificateExpiresAt}.IsDue(now)
}

func revokeOriginCertificate(ctx context.Context, front edge.Edge, id string, runProgress progress.Log) {
	certificates := front.Hooks().OriginCertificates
	if id == "" || certificates == nil {
		return
	}
	if err := certificates.Revoke(ctx, id); err != nil {
		runProgress.Warn(fmt.Sprintf("the origin certificate %s is no longer answered with, and stays valid until it expires: revoking it failed: %v", id, err))
	}
}
