package box

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/ocelhq/ocel/pkg/containerimage"
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
	"github.com/ocelhq/ocel/platform/vps/provider/live"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
	"github.com/ocelhq/ocel/platform/vps/provider/switchboard"
)

type stack struct {
	e     *Edge
	state edge.StackState
}

var _ edge.EdgeStack = (*stack)(nil)

func (s *stack) State() edge.StackState { return s.state }

func (s *stack) surface() string { return Surface(s.state.Slug, s.state.Tier) }

func (s *stack) routeKey(pointer, app string) host.RouteKey {
	return host.RouteKey{Owner: s.surface(), Pointer: router.ResolvePointer(pointer), App: app}
}

type promotable struct {
	key    host.RouteKey
	app    string
	record router.ReleaseRecord
}

func (s *stack) serve(ctx context.Context, move router.PointerMove, ready []promotable, progress progress.Log) error {
	apps := make([]host.AppRollout, 0, len(ready))
	for _, release := range ready {
		apps = append(apps, host.AppRollout{
			RouteKey:             release.key,
			Target:               release.record.Physical + ":" + containerimage.PortText,
			HealthPath:           release.record.HealthPath,
			HealthPathDiscovered: release.record.HealthPathDiscovered,
		})
	}
	claims, err := s.listPreviewClaims(ctx, move.Pointer, move.Hosts)
	if err != nil {
		return s.refuse(ctx, move, nil, nil, err)
	}
	if len(claims) > 0 || len(ready) > 0 {
		if err := s.putRecordedGate(ctx); err != nil {
			return s.refuse(ctx, move, nil, nil, err)
		}
	}
	added, err := s.claimPreview(ctx, claims)
	if err != nil {
		return s.refuse(ctx, move, nil, added, err)
	}
	var touched []host.AppRollout
	for at, release := range ready {
		ran, err := s.rerun(ctx, release, progress)
		apps[at].Run = ran
		if ran != host.RunKept {
			touched = append(touched, apps[at])
		}
		if err != nil {
			return s.refuse(ctx, move, touched, added, err)
		}
	}
	err = s.e.machine.RollOut(ctx, host.Rollout{
		Apps:          apps,
		DeployTimeout: host.DeployWindow,
		DrainTimeout:  host.DrainWindow,
		StillActive:   move.RefuseInactive,
	}, progress)
	if unserved := (router.Unserved{}); errors.As(err, &unserved) {
		return s.refuse(ctx, move, nil, added, err)
	}
	return errors.Join(err, s.recordServed(ctx, move, claims, ready))
}

func (s *stack) refuse(ctx context.Context, move router.PointerMove, touched []host.AppRollout, added []host.HostClaim, why error) error {
	ctx, stop := context.WithTimeout(context.WithoutCancel(ctx), host.UnwindWindow)
	defer stop()
	if len(touched) > 0 {
		why = fmt.Errorf("%w%s", why, s.e.machine.Discard(ctx, host.Rollout{Apps: touched}))
	}
	return router.Unserved{Err: errors.Join(why, s.disclaimAdded(ctx, move, added))}
}

func (s *stack) recordServed(ctx context.Context, move router.PointerMove, claims []host.HostClaim, ready []promotable) error {
	var errs []error
	if len(claims) > 0 {
		errs = append(errs, s.disclaimUnnamed(ctx, move.Pointer, claims))
	}
	for _, release := range ready {
		errs = append(errs, s.e.machine.Promote(ctx, s.state.Tier, s.state.Slug, release.app, release.record.Image))
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("promotion %s serves on this box, but the box did not record all of it: %w", move.Promotion.PromotionID, err)
	}
	return nil
}

func (s *stack) readyRelease(ctx context.Context, pointer, promotionID string, record router.ReleaseRecord) (promotable, bool, error) {
	app, release := record.App, record.Release
	if record.Physical == "" {
		return promotable{}, false, nil
	}
	if record.Image == "" || record.HealthPath == "" {
		return promotable{}, false, refusal.Refuse(refusal.CodeInvalid,
			"promote %s: the record for %s/%s (container %s) lacks an image (%q) or health path (%q)",
			promotionID, app, release, record.Physical, record.Image, record.HealthPath)
	}
	hasImage, err := s.e.machine.EnsureImage(ctx, s.state.Slug, app, record.Image)
	if err != nil {
		return promotable{}, false, err
	}
	if !hasImage {
		return promotable{}, false, refusal.Refuse(refusal.CodeNotReady,
			"promote %s: this box no longer has %s for %s/%s; %s is unchanged\nDeploy again",
			promotionID, record.Image, app, release, app)
	}
	return promotable{key: s.routeKey(pointer, app), app: app, record: record}, true, nil
}

func declaredBy(record router.ReleaseRecord) []string {
	declared := make([]string, 0, len(record.Variables)+len(record.Env))
	for _, variable := range record.Variables {
		declared = append(declared, variable.Key)
	}
	for name := range record.Env {
		declared = append(declared, name)
	}
	slices.Sort(declared)
	return declared
}

func (s *stack) rerun(ctx context.Context, release promotable, progress progress.Log) (host.Run, error) {
	record := release.record
	if progress != nil {
		progress.Say("Starting " + release.app + "'s container " + record.Physical + " again")
	}
	return s.e.machine.RunContainer(ctx, host.Container{
		Name: record.Physical, Project: s.state.Slug, App: release.app, Image: record.Image, Tier: s.state.Tier,
		HealthPath: record.HealthPath, Declared: declaredBy(record),
	})
}

func (s *stack) listPreviewClaims(ctx context.Context, pointer string, hosts []edge.PreviewHost) ([]host.HostClaim, error) {
	if len(hosts) == 0 || router.IsDefaultPointer(pointer) {
		return nil, nil
	}
	claims := make([]host.HostClaim, 0, len(hosts)+1)
	for _, served := range hosts {
		claims = append(claims, host.HostClaim{
			Hostname: served.Hostname, Owner: s.surface(), Pointer: pointer, App: served.App,
		})
	}
	stores, err := s.stores(ctx, router.ResolvePointer(pointer))
	if err != nil || !stores {
		return claims, err
	}
	hostname, err := formatPreviewStoreHostname(hosts)
	if err != nil {
		return nil, err
	}
	return append(claims, host.HostClaim{
		Hostname: hostname, Owner: s.surface(), Pointer: pointer, App: switchboard.StoreLabel,
	}), nil
}

func formatPreviewStoreHostname(hosts []edge.PreviewHost) (string, error) {
	first := slices.MinFunc(hosts, func(a, b edge.PreviewHost) int { return strings.Compare(a.Hostname, b.Hostname) })
	_, base, _ := strings.Cut(first.Hostname, ".")
	tail := first.ReadTail()
	if tail == "" || base == "" {
		return "", refusal.Refuse(refusal.CodeInvalid,
			"the preview hostname %s ends in no %d-character token to name its store after", first.Hostname, edge.PreviewTailLen)
	}
	return tail + "-" + switchboard.StoreLabel + "." + base, nil
}

func (s *stack) claimPreview(ctx context.Context, claims []host.HostClaim) ([]host.HostClaim, error) {
	if len(claims) == 0 {
		return nil, nil
	}
	current, err := s.e.machine.Claims(ctx)
	if err != nil {
		return nil, err
	}
	added := slices.DeleteFunc(slices.Clone(claims), func(claim host.HostClaim) bool { return slices.Contains(current, claim) })
	return added, s.claim(ctx, claims)
}

func (s *stack) disclaimAdded(ctx context.Context, move router.PointerMove, added []host.HostClaim) error {
	if len(added) == 0 {
		return nil
	}
	hostnames := make([]string, 0, len(added))
	for _, claim := range added {
		hostnames = append(hostnames, claim.Hostname)
	}
	if err := move.RefuseInactive(ctx); err != nil {
		return fmt.Errorf("%s left claimed: %w", strings.Join(hostnames, ", "), err)
	}
	var errs []error
	for _, claim := range added {
		if err := s.e.machine.DisclaimHost(ctx, claim.Hostname, s.surface()); err != nil {
			errs = append(errs, fmt.Errorf("%s left claimed: %w", claim.Hostname, err))
		}
	}
	if err := s.applyOrigins(ctx); err != nil {
		errs = append(errs, s.released(strings.Join(hostnames, ", "), err))
	}
	return errors.Join(errs...)
}

func (s *stack) disclaimUnnamed(ctx context.Context, pointer string, claims []host.HostClaim) error {
	current, err := s.e.machine.Claims(ctx)
	if err != nil {
		return err
	}
	for _, claim := range current {
		if claim.Owner != s.surface() || claim.Pointer != pointer ||
			slices.ContainsFunc(claims, func(named host.HostClaim) bool { return named.Hostname == claim.Hostname }) {
			continue
		}
		if err := s.e.machine.DisclaimHost(ctx, claim.Hostname, s.surface()); err != nil {
			return err
		}
	}
	return nil
}

func (s *stack) BindDomain(ctx context.Context, binding edge.DomainBinding) error {
	origin, err := s.claimHostname(ctx, router.Claim{Hostname: binding.Hostname, App: binding.App})
	if err != nil {
		return err
	}
	s.state.Bind(binding.Hostname)
	s.state.PublishAddress(binding.Hostname, origin.Address)
	if route := s.e.machine.RouteBy(binding.Hostname); route != "" && binding.Say != nil {
		binding.Say(route)
	}
	return nil
}

func (s *stack) claimHostname(ctx context.Context, claim router.Claim) (edge.Origin, error) {
	if claim.Hostname == "" {
		return edge.Origin{}, refusal.Refuse(refusal.CodeInvalid, "this binding names no hostname for %s to claim", s.surface())
	}
	if err := s.putRecordedGate(ctx); err != nil {
		return edge.Origin{}, err
	}
	if claim.Tunnel != edge.None {
		return s.claimTunneled(ctx, claim)
	}
	address, err := s.e.machine.Address(ctx)
	if err != nil {
		return edge.Origin{}, err
	}
	shielded := len(claim.ClientCAs) > 0
	certified := true
	if shielded {
		if certified, err = s.e.putShield(ctx, claim, s.surface()); err != nil {
			return edge.Origin{}, err
		}
	}
	served, err := s.listServed(ctx, claim)
	if err != nil {
		return edge.Origin{}, err
	}
	if err := s.claim(ctx, served); err != nil {
		return edge.Origin{}, err
	}
	if err := s.e.removeTunneledHosts(ctx, s.surface(), claim.Hostname, live.StoreHostname(claim.Hostname)); err != nil {
		return edge.Origin{}, err
	}
	if shielded {
		if err := s.e.machine.RefuseUnshielded(ctx, claim.Hostname); err != nil {
			return edge.Origin{}, err
		}
	}
	return edge.Origin{Address: address, Certified: certified}, nil
}

func (s *stack) claimTunneled(ctx context.Context, claim router.Claim) (edge.Origin, error) {
	served, err := s.listServed(ctx, claim)
	if err != nil {
		return edge.Origin{}, err
	}
	var stores []string
	for _, claimed := range served {
		if claimed.App == switchboard.StoreLabel {
			stores = append(stores, claimed.Hostname)
		}
	}
	origin, err := s.e.tunnelHost(ctx, claim.Hostname, stores, claim.Tunnel, s.surface())
	if err != nil {
		return edge.Origin{}, err
	}
	return origin, s.claim(ctx, served)
}

func (s *stack) listServed(ctx context.Context, claim router.Claim) ([]host.HostClaim, error) {
	if strings.HasPrefix(claim.Hostname, "*.") {
		return nil, nil
	}
	served := []host.HostClaim{{
		Hostname: claim.Hostname, Owner: s.surface(), Pointer: router.DefaultPointer, App: claim.App,
	}}
	stores, err := s.stores(ctx, router.DefaultPointer)
	if err != nil || !stores {
		return served, err
	}
	return append(served, host.HostClaim{
		Hostname: live.StoreHostname(claim.Hostname), Owner: s.surface(),
		Pointer: router.DefaultPointer, App: switchboard.StoreLabel,
	}), nil
}

func (e *Edge) putShield(ctx context.Context, claim router.Claim, owner string) (certified bool, err error) {
	held, err := e.machine.PutShield(ctx, host.Shield{
		Hostname: claim.Hostname, Owner: owner, ClientCAs: claim.ClientCAs,
		OriginCertificate: proxy.CertificatePair{Certificate: claim.OriginCertificate.Certificate, Key: claim.OriginCertificate.Key},
	})
	if err != nil {
		return false, err
	}
	return isOriginCertificateCurrent(held, time.Now()), nil
}

func isOriginCertificateCurrent(shield host.Shield, now time.Time) bool {
	block, _ := pem.Decode([]byte(shield.OriginCertificate.Certificate))
	if block == nil {
		return false
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil || leaf.VerifyHostname(shield.Hostname) != nil {
		return false
	}
	return !edge.OriginCertificate{ExpiresAt: leaf.NotAfter}.IsDue(now)
}

func (s *stack) claim(ctx context.Context, claims []host.HostClaim) error {
	if len(claims) == 0 {
		return nil
	}
	if err := s.e.machine.ClaimHosts(ctx, claims); err != nil {
		return err
	}
	return s.applyOrigins(ctx)
}

func (s *stack) applyOrigins(ctx context.Context) error {
	return s.e.origins(ctx, s.state.Slug, s.state.Tier)
}

func (s *stack) stores(ctx context.Context, pointer string) (bool, error) {
	upstream, err := s.e.machine.Serving(ctx, s.routeKey(pointer, switchboard.StoreLabel))
	return upstream != "", err
}

func (s *stack) UnbindDomain(ctx context.Context, hostname string) error {
	if err := s.disclaimHostname(ctx, hostname); err != nil {
		return err
	}
	if err := s.e.releaseTunnel(ctx); err != nil {
		return err
	}
	s.state.Release(hostname)
	s.state.PublishAddress(hostname, "")
	if err := s.applyOrigins(ctx); err != nil {
		return progress.MarkWarning(s.released(hostname, err))
	}
	return nil
}

func (s *stack) disclaimHostname(ctx context.Context, hostname string) error {
	if err := s.e.machine.DisclaimHost(ctx, hostname, s.surface()); err != nil {
		return err
	}
	if err := s.e.machine.DisclaimHost(ctx, live.StoreHostname(hostname), s.surface()); err != nil {
		return err
	}
	return s.e.machine.RemoveShield(ctx, hostname, s.surface())
}

func (s *stack) released(what string, err error) error {
	return fmt.Errorf("%s is released, but this project's buckets still answer it as an origin until the next deploy brings them in line with what the project claims: %w", what, err)
}

func (s *stack) Destroy(ctx context.Context) error {
	var errs []error
	if err := s.e.machine.DisclaimSurface(ctx, s.surface()); err != nil {
		errs = append(errs, err)
	} else {
		for _, hostname := range slices.Clone(s.state.Bound) {
			s.state.Release(hostname)
			s.state.PublishAddress(hostname, "")
		}
	}
	if err := s.e.machine.UnrouteSurface(ctx, s.surface()); err != nil {
		errs = append(errs, err)
	}
	if err := s.e.machine.ForgetNetwork(ctx, s.state.Tier, s.state.Slug); err != nil {
		errs = append(errs, err)
	}
	if err := s.e.releaseTunnel(ctx); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}
