package switchboard

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
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/release"
	"github.com/ocelhq/ocel/platform/vps/provider/routing"
)

type stack struct {
	e     *Edge
	state edge.StackState
}

var _ edge.EdgeStack = (*stack)(nil)

func (s *stack) State() edge.StackState { return s.state }

func (s *stack) surface() string { return Surface(s.state.Slug, s.state.Tier) }

func (s *stack) routeKey(pointer, app string) boxapi.RouteKey {
	return boxapi.RouteKey{Owner: s.surface(), Pointer: router.ResolvePointer(pointer), App: app}
}

type promotable struct {
	key    boxapi.RouteKey
	app    string
	record router.ReleaseRecord
}

func (s *stack) serve(ctx context.Context, move router.PointerMove, ready []promotable, progress progress.Log) error {
	apps := make([]release.AppRollout, 0, len(ready))
	for _, promoted := range ready {
		apps = append(apps, release.AppRollout{
			RouteKey:             promoted.key,
			Target:               promoted.record.Physical + ":" + containerimage.PortText,
			HealthPath:           promoted.record.HealthPath,
			HealthPathDiscovered: promoted.record.HealthPathDiscovered,
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
	var touched []release.AppRollout
	for at, promoted := range ready {
		ran, err := s.rerun(ctx, promoted, progress)
		apps[at].Run = ran
		if ran != release.RunKept {
			touched = append(touched, apps[at])
		}
		if err != nil {
			return s.refuse(ctx, move, touched, added, err)
		}
	}
	err = s.e.containers.RollOut(ctx, release.Rollout{
		Apps:          apps,
		DeployTimeout: routing.DeployWindow,
		DrainTimeout:  routing.DrainWindow,
		StillActive:   move.RefuseInactive,
	}, progress)
	if unserved := (router.Unserved{}); errors.As(err, &unserved) {
		return s.refuse(ctx, move, nil, added, err)
	}
	return errors.Join(err, s.recordServed(ctx, move, claims, ready))
}

func (s *stack) refuse(ctx context.Context, move router.PointerMove, touched []release.AppRollout, added []boxapi.Claim, why error) error {
	ctx, stop := context.WithTimeout(context.WithoutCancel(ctx), release.UnwindWindow)
	defer stop()
	if len(touched) > 0 {
		why = fmt.Errorf("%w%s", why, s.e.containers.Discard(ctx, release.Rollout{Apps: touched}))
	}
	return router.Unserved{Err: errors.Join(why, s.disclaimAdded(ctx, move, added))}
}

func (s *stack) recordServed(ctx context.Context, move router.PointerMove, claims []boxapi.Claim, ready []promotable) error {
	var errs []error
	if len(claims) > 0 {
		errs = append(errs, s.disclaimUnnamed(ctx, move.Pointer, claims))
	}
	for _, promoted := range ready {
		errs = append(errs, s.e.images.Promote(ctx, s.state.Tier, s.state.Slug, promoted.app, promoted.record.Image))
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("promotion %s serves on this box, but the box did not record all of it: %w", move.Promotion.PromotionID, err)
	}
	return nil
}

func (s *stack) readyRelease(ctx context.Context, pointer, promotionID string, record router.ReleaseRecord) (promotable, bool, error) {
	app, recorded := record.App, record.Release
	if record.Physical == "" {
		return promotable{}, false, nil
	}
	if record.Image == "" || record.HealthPath == "" {
		return promotable{}, false, refusal.Refuse(refusal.CodeInvalid,
			"promote %s: the record for %s/%s (container %s) lacks an image (%q) or health path (%q)",
			promotionID, app, recorded, record.Physical, record.Image, record.HealthPath)
	}
	hasImage, err := s.e.images.Ensure(ctx, s.state.Slug, app, record.Image)
	if err != nil {
		return promotable{}, false, err
	}
	if !hasImage {
		return promotable{}, false, refusal.Refuse(refusal.CodeNotReady,
			"promote %s: this box no longer has %s for %s/%s; %s is unchanged\nDeploy again",
			promotionID, record.Image, app, recorded, app)
	}
	return promotable{key: s.routeKey(pointer, app), app: app, record: record}, true, nil
}

func declaredBy(record router.ReleaseRecord) []string {
	declared := make([]string, 0, len(record.Variables))
	for _, variable := range record.Variables {
		declared = append(declared, variable.Key)
	}
	slices.Sort(declared)
	return declared
}

func (s *stack) rerun(ctx context.Context, promoted promotable, progress progress.Log) (release.Run, error) {
	record := promoted.record
	progress.Say("Starting " + promoted.app + "'s container " + record.Physical + " again")
	return s.e.containers.Run(ctx, release.Container{
		Name: record.Physical, Project: s.state.Slug, App: promoted.app, Image: record.Image, Tier: s.state.Tier,
		HealthPath: record.HealthPath, Declared: declaredBy(record),
	})
}

func (s *stack) listPreviewClaims(ctx context.Context, pointer string, hosts []edge.PreviewHost) ([]boxapi.Claim, error) {
	if len(hosts) == 0 || router.IsDefaultPointer(pointer) {
		return nil, nil
	}
	claims := make([]boxapi.Claim, 0, len(hosts)+1)
	for _, served := range hosts {
		claims = append(claims, boxapi.Claim{
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
	return append(claims, boxapi.Claim{
		Hostname: hostname, Owner: s.surface(), Pointer: pointer, App: boxapi.StoreLabel,
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
	return tail + "-" + boxapi.StoreLabel + "." + base, nil
}

func (s *stack) claimPreview(ctx context.Context, claims []boxapi.Claim) ([]boxapi.Claim, error) {
	if len(claims) == 0 {
		return nil, nil
	}
	current, err := s.e.routes.Claims(ctx)
	if err != nil {
		return nil, err
	}
	added := slices.DeleteFunc(slices.Clone(claims), func(claim boxapi.Claim) bool { return slices.Contains(current, claim) })
	return added, s.claim(ctx, claims)
}

func (s *stack) disclaimAdded(ctx context.Context, move router.PointerMove, added []boxapi.Claim) error {
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
		if err := s.e.routes.DisclaimHost(ctx, claim.Hostname, s.surface()); err != nil {
			errs = append(errs, fmt.Errorf("%s left claimed: %w", claim.Hostname, err))
		}
	}
	if err := s.applyOrigins(ctx); err != nil {
		errs = append(errs, s.released(strings.Join(hostnames, ", "), err))
	}
	return errors.Join(errs...)
}

func (s *stack) disclaimUnnamed(ctx context.Context, pointer string, claims []boxapi.Claim) error {
	current, err := s.e.routes.Claims(ctx)
	if err != nil {
		return err
	}
	for _, claim := range current {
		if claim.Owner != s.surface() || claim.Pointer != pointer ||
			slices.ContainsFunc(claims, func(named boxapi.Claim) bool { return named.Hostname == claim.Hostname }) {
			continue
		}
		if err := s.e.routes.DisclaimHost(ctx, claim.Hostname, s.surface()); err != nil {
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
	if err := s.e.releaseTunnel(ctx); err != nil {
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
	origin, err := s.e.claimThrough(ctx, claim.Tunnel, s.surface(), claim.Hostname, func(tunnel boxapi.Tunnel) error {
		return s.e.routes.ClaimThrough(ctx, tunnel.Name, served)
	})
	if err != nil {
		return edge.Origin{}, err
	}
	return origin, s.applyOrigins(ctx)
}

func (s *stack) listServed(ctx context.Context, claim router.Claim) ([]boxapi.Claim, error) {
	if strings.HasPrefix(claim.Hostname, "*.") {
		return nil, nil
	}
	served := []boxapi.Claim{{
		Hostname: claim.Hostname, Owner: s.surface(), Pointer: router.DefaultPointer, App: claim.App,
	}}
	stores, err := s.stores(ctx, router.DefaultPointer)
	if err != nil || !stores {
		return served, err
	}
	return append(served, boxapi.Claim{
		Hostname: boxapi.StoreHostname(claim.Hostname), Owner: s.surface(),
		Pointer: router.DefaultPointer, App: boxapi.StoreLabel,
	}), nil
}

func (e *Edge) putShield(ctx context.Context, claim router.Claim, owner string) (certified bool, err error) {
	held, err := e.routes.PutShield(ctx, boxapi.Shield{
		Hostname: claim.Hostname, Owner: owner, ClientCAs: claim.ClientCAs,
		OriginCertificate: boxapi.CertificatePair{Certificate: claim.OriginCertificate.Certificate, Key: claim.OriginCertificate.Key},
	})
	if err != nil {
		return false, err
	}
	return isOriginCertificateCurrent(held, time.Now()), nil
}

func isOriginCertificateCurrent(shield boxapi.Shield, now time.Time) bool {
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

func (s *stack) claim(ctx context.Context, claims []boxapi.Claim) error {
	if len(claims) == 0 {
		return nil
	}
	if err := s.e.routes.ClaimHosts(ctx, claims); err != nil {
		return err
	}
	return s.applyOrigins(ctx)
}

func (s *stack) applyOrigins(ctx context.Context) error {
	return s.e.origins(ctx, s.state.Slug, s.state.Tier)
}

func (s *stack) stores(ctx context.Context, pointer string) (bool, error) {
	upstream, err := s.e.routes.Serving(ctx, s.routeKey(pointer, boxapi.StoreLabel))
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
	if err := s.e.routes.DisclaimHost(ctx, hostname, s.surface()); err != nil {
		return err
	}
	if err := s.e.routes.DisclaimHost(ctx, boxapi.StoreHostname(hostname), s.surface()); err != nil {
		return err
	}
	return s.e.routes.RemoveShield(ctx, hostname, s.surface())
}

func (s *stack) released(what string, err error) error {
	return fmt.Errorf("%s is released, but this project's buckets still answer it as an origin until the next deploy brings them in line with what the project claims: %w", what, err)
}

func (s *stack) Destroy(ctx context.Context) error {
	var errs []error
	if err := s.e.routes.DisclaimSurface(ctx, s.surface()); err != nil {
		errs = append(errs, err)
	} else {
		for _, hostname := range slices.Clone(s.state.Bound) {
			s.state.Release(hostname)
			s.state.PublishAddress(hostname, "")
		}
	}
	if err := s.e.routes.UnrouteSurface(ctx, s.surface()); err != nil {
		errs = append(errs, err)
	}
	if err := s.e.containers.ForgetNetwork(ctx, s.state.Tier, s.state.Slug); err != nil {
		errs = append(errs, err)
	}
	if err := s.e.releaseTunnel(ctx); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}
