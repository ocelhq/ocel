package box

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/ocelhq/ocel/pkg/appbuild"
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
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
	record router.DeploymentRecord
}

func (s *stack) serve(ctx context.Context, flip router.Flip, ready []promotable, progress progress.Progress) error {
	pointer := flip.Pointer
	claims, err := s.previewClaims(ctx, pointer, slices.Sorted(maps.Keys(flip.Records)))
	if err != nil {
		return router.Unserved{Err: err}
	}
	if err := s.claim(ctx, claims); err != nil {
		return router.Unserved{Err: err}
	}
	apps := make([]host.AppRelease, 0, len(ready))
	for _, release := range ready {
		if err := s.rerun(ctx, release, progress); err != nil {
			return router.Unserved{Err: err}
		}
		apps = append(apps, host.AppRelease{
			RouteKey:   release.key,
			Target:     release.record.Physical + ":" + appbuild.InjectedPortText,
			HealthPath: release.record.HealthPath,
		})
	}
	return s.e.machine.Release(ctx, host.Release{
		Apps:          apps,
		DeployTimeout: host.DeployWindow,
		DrainTimeout:  host.DrainWindow,
		StillActive:   flip.RefuseInactive,
	}, progress)
}

func (s *stack) readyRelease(ctx context.Context, pointer, promotionID string, record router.DeploymentRecord) (promotable, bool, error) {
	app, identity := record.App, record.Build
	if record.Physical == "" {
		return promotable{}, false, nil
	}
	if record.Image == "" || record.HealthPath == "" {
		return promotable{}, false, refusal.Refuse(refusal.CodeInvalid,
			"promote %s: the record for %s/%s (container %s) lacks an image (%q) or health path (%q)",
			promotionID, app, identity, record.Physical, record.Image, record.HealthPath)
	}
	hasImage, err := s.e.machine.HasImage(ctx, record.Image)
	if err != nil {
		return promotable{}, false, err
	}
	if !hasImage {
		return promotable{}, false, refusal.Refuse(refusal.CodeNotReady,
			"promote %s: this box no longer has %s for %s/%s; %s is unchanged\nDeploy again",
			promotionID, record.Image, app, identity, app)
	}
	return promotable{key: s.routeKey(pointer, app), app: app, record: record}, true, nil
}

func declaredBy(record router.DeploymentRecord) []string {
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

func (s *stack) rerun(ctx context.Context, release promotable, progress progress.Progress) error {
	record := release.record
	if progress != nil {
		progress.Say("Starting " + release.app + "'s container " + record.Physical + " again")
	}
	if err := s.e.machine.RunContainer(ctx, host.Container{
		Name: record.Physical, Project: s.state.Slug, App: release.app, Image: record.Image, Tier: s.state.Tier,
		HealthPath: record.HealthPath, Declared: declaredBy(record),
	}); err != nil {
		return err
	}
	return s.e.machine.Promote(ctx, s.state.Tier, s.state.Slug, release.app, record.Image)
}

func (s *stack) previewSite() edge.PreviewSite {
	if s.state.Tier != environment.TierPreview {
		return edge.PreviewSite{}
	}
	if s.state.GlobalPreview == "" && s.state.PreviewBase != "" {
		return edge.ProjectPreview(s.state.PreviewBase)
	}
	return edge.SharedPreview(s.state.Slug, s.state.GlobalPreview)
}

func (s *stack) previewClaims(ctx context.Context, pointer string, apps []string) ([]host.HostClaim, error) {
	site := s.previewSite()
	if !site.Serves() || len(apps) == 0 || router.IsDefaultPointer(pointer) {
		return nil, nil
	}
	stores, err := s.stores(ctx, router.ResolvePointer(pointer))
	if err != nil {
		return nil, err
	}
	hostnames := site.Hosts(pointer, apps)
	if stores {
		hostnames = append(hostnames, site.Host(pointer, switchboard.StoreLabel))
	}
	if err := site.LabelProblem(hostnames); err != nil {
		return nil, refusal.Refuse(refusal.CodeInvalid,
			"%s claims no preview hostname on this box: %s", s.surface(), err)
	}
	claims := make([]host.HostClaim, 0, len(hostnames))
	for _, hostname := range hostnames {
		app := ""
		if at := slices.IndexFunc(apps, func(app string) bool { return site.Host(pointer, app) == hostname }); at >= 0 {
			app = apps[at]
		}
		if hostname == site.Host(pointer, switchboard.StoreLabel) {
			app = switchboard.StoreLabel
		}
		claims = append(claims, host.HostClaim{
			Hostname: hostname, Owner: s.surface(), Pointer: pointer, App: app,
		})
	}
	return claims, nil
}

func (s *stack) BindDomain(ctx context.Context, binding edge.DomainBinding) error {
	address, _, err := s.claimHostname(ctx, router.Claim{Hostname: binding.Hostname, App: binding.App})
	if err != nil {
		return err
	}
	s.state.Bind(binding.Hostname)
	s.state.PublishFront(binding.Hostname, address)
	if route := s.e.machine.RouteBy(binding.Hostname); route != "" && binding.Say != nil {
		binding.Say(route)
	}
	return nil
}

func (s *stack) claimHostname(ctx context.Context, claim router.Claim) (address string, certified bool, err error) {
	if claim.Hostname == "" {
		return "", false, refusal.Refuse(refusal.CodeInvalid, "this binding names no hostname for %s to claim", s.surface())
	}
	if address, err = s.e.machine.Address(ctx); err != nil {
		return "", false, err
	}
	shielded := len(claim.ClientCertificates) > 0
	certified = true
	if shielded {
		if certified, err = s.e.putShield(ctx, claim, s.surface()); err != nil {
			return "", false, err
		}
	}
	if _, wild := strings.CutPrefix(claim.Hostname, "*."); !wild {
		if err := s.claimServed(ctx, claim); err != nil {
			return "", false, err
		}
	}
	if shielded {
		if err := s.e.machine.RefuseUnshielded(ctx, claim.Hostname); err != nil {
			return "", false, err
		}
	}
	return address, certified, nil
}

func (s *stack) claimServed(ctx context.Context, claim router.Claim) error {
	claims := []host.HostClaim{{
		Hostname: claim.Hostname, Owner: s.surface(), Pointer: router.DefaultPointer, App: claim.App,
	}}
	stores, err := s.stores(ctx, router.DefaultPointer)
	if err != nil {
		return err
	}
	if stores {
		claims = append(claims, host.HostClaim{
			Hostname: live.StoreHostname(claim.Hostname), Owner: s.surface(),
			Pointer: router.DefaultPointer, App: switchboard.StoreLabel,
		})
	}
	return s.claim(ctx, claims)
}

func (e *Edge) putShield(ctx context.Context, claim router.Claim, owner string) (certified bool, err error) {
	held, err := e.machine.PutShield(ctx, host.Shield{
		Hostname: claim.Hostname, Owner: owner, ClientCertificates: claim.ClientCertificates,
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
	s.state.Release(hostname)
	s.state.PublishFront(hostname, "")
	if err := s.applyOrigins(ctx); err != nil {
		return progress.Warned(s.released(hostname, err))
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
			s.state.PublishFront(hostname, "")
		}
	}
	if err := s.e.machine.UnrouteSurface(ctx, s.surface()); err != nil {
		errs = append(errs, err)
	}
	if err := s.e.machine.ForgetNetwork(ctx, s.state.Tier, s.state.Slug); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}
