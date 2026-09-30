package providerserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	planv1 "github.com/ocelhq/ocel/pkg/proto/common/plan/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/bootstrapplan"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

type wildcards struct {
	provider       provider.Provider
	keyValues      keyvalue.Store
	recorded       stackrecords.Wildcard
	sel            *contractv1.EdgeSelection
	progressStream *eventStream
	progressSpan   Span
}

func (h *handlers) wildcard(ctx context.Context, sel *contractv1.EdgeSelection) (*wildcards, error) {
	vendor, err := h.session.use()
	if err != nil {
		return nil, err
	}
	store := vendor.KeyValues()
	wildcard, err := stackrecords.ReadWildcard(ctx, store)
	if err != nil {
		return nil, err
	}
	return &wildcards{provider: vendor, keyValues: store, recorded: wildcard, sel: sel}, nil
}

func (w *wildcards) dnsCutover(front edge.Edge) (dnsCutover, error) {
	writer, err := dnsFor(w.provider, front, w.sel)
	if err != nil {
		return dnsCutover{}, err
	}
	s := newDNSCutover(front, writer, w.sel.GetDns().GetZone(), w.provider.Liveness())
	if w.progressStream != nil {
		s.waitForManualRecords(w.progressStream, w.progressSpan)
	}
	return s, nil
}

func (w *wildcards) save(ctx context.Context) error {
	name := stackrecords.WildcardKey(environment.TierPreview)
	for range reservationAttempts {
		entry, stored, err := w.readStored(ctx)
		if err != nil {
			return err
		}
		if !w.recorded.Host.Tunneled {
			w.recorded.Host.OriginCertificateID = stored.Host.OriginCertificateID
			w.recorded.Host.OriginCertificateExpiresAt = stored.Host.OriginCertificateExpiresAt
		}
		if entry.Value, err = json.Marshal(w.recorded); err != nil {
			return fmt.Errorf("record %s: %w", name, err)
		}
		_, err = w.keyValues.Write(ctx, entry)
		if errors.Is(err, keyvalue.ErrStale) {
			continue
		}
		if err != nil {
			return fmt.Errorf("record %s: %w", name, err)
		}
		return nil
	}
	return fmt.Errorf("record %s: it changed under every one of %d attempts", name, reservationAttempts)
}

func (w *wildcards) readStored(ctx context.Context) (keyvalue.Entry, stackrecords.Wildcard, error) {
	name := stackrecords.WildcardKey(environment.TierPreview)
	entry, err := keyvalue.ReadOrEmpty(ctx, w.keyValues, name)
	if err != nil {
		return keyvalue.Entry{}, stackrecords.Wildcard{}, fmt.Errorf("read %s: %w", name, err)
	}
	var stored stackrecords.Wildcard
	if len(entry.Value) > 0 {
		if err := json.Unmarshal(entry.Value, &stored); err != nil {
			return keyvalue.Entry{}, stackrecords.Wildcard{}, fmt.Errorf("read %s: %w", name, err)
		}
	}
	return entry, stored, nil
}

func (h *handlers) UsePreviewWildcard(ctx context.Context, req *contractv1.UsePreviewWildcardRequest, stream *connect.ServerStream[progressv1.OperationEvent]) error {
	root := RootSpan(naming.SpanEdge, string(environment.TierPreview),
		progress.Serving.Title("every project's previews on "+edge.PreviewWildcard(req.GetBaseDomain())), progressv1.Phase_PHASE_PROVISION)
	return streamed(ctx, stream, root, func(sender *eventStream, progress progress.Log) error {
		base, err := previewBaseDomain(req.GetBaseDomain())
		if err != nil {
			return err
		}
		w, err := h.wildcard(ctx, req.GetEdge())
		if err != nil {
			return err
		}
		w.progressStream, w.progressSpan = sender, root
		front, err := h.edgeFor(w.provider, req.GetEdge())
		if err != nil {
			return err
		}
		return w.use(ctx, front, base, progress)
	})
}

func (w *wildcards) use(ctx context.Context, front edge.Edge, base string, progress progress.Log) error {
	if err := w.claimable(front, base); err != nil {
		return err
	}
	cutover, err := w.dnsCutover(front)
	if err != nil {
		return err
	}
	answering, err := findPairedRouter(w.provider, front.Kind())
	if err != nil {
		return err
	}
	wildcard := edge.PreviewWildcard(base)
	w.recorded = stackrecords.Wildcard{
		BaseDomain: base,
		Edge:       front.Kind(),
		Scope:      front.Facts().CredentialScope,
		GrammarMin: edge.PreviewGrammarMin,
		GrammarMax: edge.PreviewGrammarMax,
		Host:       w.recorded.Host,
	}

	certifying := w.hostCertificates(cutover, fmt.Sprintf(
		"If this run gives up waiting, re-run `ocel domain use '%s' --preview`.", wildcard))
	if err := certifying.certify(ctx, wildcard, progress); err != nil {
		return err
	}

	origin, err := w.entryOrigin(front)
	if err != nil {
		return err
	}
	progress.Say("Reconciling the shared preview entry on " + wildcard)
	published, err := w.reconcileEntry(ctx, front, origin, progress)
	if err != nil {
		return err
	}
	if err := certifying.discardSuperseded(ctx, progress); err != nil {
		return err
	}

	var dnsRecords []edge.Record
	if origin == nil {
		target := edge.DNSTarget{Kind: front.Kind(), ServesUnbound: front.Facts().ServesUnbound, ProxiesRecords: front.Facts().ProxiesRecords, Address: published}
		if dnsRecords, err = edge.RecordsFor(target, []string{wildcard}); err != nil {
			return err
		}
	}
	written, werr := cutover.write(ctx, dnsRecords,
		fmt.Sprintf("Point %s at %s", wildcard, describeFront(front.Kind())), progress.Say,
		fmt.Sprintf("If this run gives up waiting, re-run `ocel domain use '%s' --preview`.", wildcard))
	w.recorded.Host.Written, w.recorded.Host.Manual = written.Written, written.Manual
	if err := w.save(ctx); err != nil {
		return errors.Join(werr, err)
	}
	if werr != nil {
		return werr
	}

	probe, aerr := cutover.await(ctx, wildcard, answering, progress.Say)
	w.recorded.Host.Probe = probe
	if err := w.save(ctx); err != nil {
		return errors.Join(aerr, err)
	}
	if aerr != nil {
		return aerr
	}
	progress.Say(fmt.Sprintf("Previews are served on %s through %s", wildcard, describeFront(front.Kind())))
	return nil
}

func (w *wildcards) entryOrigin(front edge.Edge) (*router.OriginHooks, error) {
	if facts := front.Facts(); !facts.ProxiesRecords || facts.RunsCode {
		return nil, nil
	}
	paired, err := openPairedRouter(w.provider, front.Kind())
	if err != nil {
		return nil, err
	}
	return routerOriginBehind(front, paired), nil
}

func (w *wildcards) reconcileEntry(ctx context.Context, front edge.Edge, origin *router.OriginHooks, runProgress progress.Log) (string, error) {
	base := w.recorded.BaseDomain
	spec := edge.PreviewWildcardSpec{
		BaseDomain:  base,
		Certificate: w.recorded.Host.Certificate.ID,
		GrammarMin:  edge.PreviewGrammarMin,
		GrammarMax:  edge.PreviewGrammarMax,
		Warn:        runProgress.Warn,
	}
	var claimed originClaim
	if origin != nil {
		var err error
		claim := router.Claim{Hostname: w.recorded.Hostname(), Certificate: w.recorded.Host.Certificate.ID, Tunnel: tunnelThrough(front, w.sel)}
		if claimed, err = claimOrigin(ctx, front, claim, origin.ClaimPreviewEntry, w.reserveOriginCertificate); err != nil {
			return "", err
		}
		spec.Origin = claimed.origin
	} else {
		program, err := edgeProgramFor(ctx, w.provider, front, provider.EdgeProgramRequest{
			Tier:              environment.TierPreview,
			PreviewBaseDomain: base,
		})
		if err != nil {
			return "", err
		}
		spec.Program, spec.Values = program.Spec, program.Values
	}
	published, err := front.ReconcilePreviewWildcard(ctx, spec)
	if err != nil {
		return "", err
	}
	superseded := claimed.recordOn(&w.recorded.Host)
	if err := w.save(ctx); err != nil {
		return "", err
	}
	revokeOriginCertificate(ctx, front, superseded, runProgress)
	return published, nil
}

var errPreviewEntryRenewing = errors.New("another run is renewing the certificate the shared preview entry is answered with")

const reservationAttempts = 8

func (w *wildcards) reserveOriginCertificate(ctx context.Context, issued edge.OriginCertificate) (func(context.Context) error, error) {
	prior := w.recorded.Host
	if err := w.swapOriginCertificate(ctx, prior.OriginCertificateID, issued.ID, issued.ExpiresAt); err != nil {
		return nil, err
	}
	return func(ctx context.Context) error {
		return w.swapOriginCertificate(ctx, issued.ID, prior.OriginCertificateID, prior.OriginCertificateExpiresAt)
	}, nil
}

func (w *wildcards) swapOriginCertificate(ctx context.Context, from, to string, expiresAt time.Time) error {
	name := stackrecords.WildcardKey(environment.TierPreview)
	for range reservationAttempts {
		entry, recorded, err := w.readStored(ctx)
		if err != nil {
			return err
		}
		if recorded.Host.OriginCertificateID != from {
			return fmt.Errorf("%w on %s: deploy again once it finishes", errPreviewEntryRenewing, w.recorded.Hostname())
		}
		recorded.Host.OriginCertificateID, recorded.Host.OriginCertificateExpiresAt = to, expiresAt
		if entry.Value, err = json.Marshal(recorded); err != nil {
			return fmt.Errorf("record %s: %w", name, err)
		}
		_, err = w.keyValues.Write(ctx, entry)
		if errors.Is(err, keyvalue.ErrStale) {
			continue
		}
		if err != nil {
			return fmt.Errorf("record %s: %w", name, err)
		}
		return nil
	}
	return fmt.Errorf("%w on %s: its record changed under every one of %d attempts", errPreviewEntryRenewing, w.recorded.Hostname(), reservationAttempts)
}

func (w *wildcards) refreshEntryClaim(ctx context.Context, front edge.Edge, runProgress progress.Log) error {
	if !w.recorded.IsRecorded() || w.recorded.Edge != front.Kind() {
		return nil
	}
	origin, err := w.entryOrigin(front)
	if err != nil || origin == nil {
		return err
	}
	stale, why, err := isOriginClaimStale(ctx, front, w.recorded.Hostname(), tunnelThrough(front, w.sel), &w.recorded.Host, time.Now())
	if err != nil || !stale {
		return err
	}
	runProgress.Say("Claiming the shared preview entry on " + w.recorded.Hostname() + " again: " + why)
	_, err = w.reconcileEntry(ctx, front, origin, runProgress)
	if errors.Is(err, errPreviewEntryRenewing) {
		runProgress.Say(err.Error())
		return nil
	}
	return err
}

func (w *wildcards) hostCertificates(cutover dnsCutover, notes ...string) hostCertificates {
	return hostCertificates{
		provider:  w.provider,
		cutover:   cutover,
		hostState: &w.recorded.Host,
		persist:   w.save,
		notes:     notes,
	}
}

func (w *wildcards) claimable(front edge.Edge, base string) error {
	if w.recorded.BaseDomain != "" && w.recorded.BaseDomain != base {
		return refusal.Refuse(refusal.CodeNotReady,
			"this preview bootstrap already serves previews on %q: release it with `ocel domain release --preview` first, then use %q — every project on %q loses its preview hostnames the moment the bootstrap changes domain, so that is two deliberate commands",
			w.recorded.BaseDomain, base, w.recorded.BaseDomain)
	}
	owningEdge := w.recorded.Edge
	if !w.recorded.IsRecorded() || owningEdge == front.Kind() {
		return nil
	}
	return refusal.Refuse(refusal.CodeNotReady,
		"%s is already served through %s, and this project is served through %s: reconciling it here would raise a second wildcard through %s and leave the one through %s in place with nothing left to name it — release it with `ocel domain release --preview` from a project served through %s first, then use it again from here",
		w.recorded.Hostname(), describeFront(owningEdge), describeFront(front.Kind()), describeFront(front.Kind()), describeFront(owningEdge), describeFront(owningEdge))
}

func (h *handlers) GetPreviewWildcard(ctx context.Context, req *contractv1.PreviewWildcardRequest) (*contractv1.GetPreviewWildcardResponse, error) {
	w, err := h.wildcard(ctx, req.GetEdge())
	if err != nil {
		return nil, provider.RefusalError(err)
	}
	if w.recorded.BaseDomain == "" {
		return &contractv1.GetPreviewWildcardResponse{}, nil
	}
	served, err := w.served(ctx)
	if err != nil {
		return nil, provider.RefusalError(err)
	}
	health, err := w.provider.Certificates().Inspect(ctx, w.recorded.Edge, w.recorded.Hostname(), w.recorded.Host.Certificate)
	if err != nil {
		return nil, provider.RefusalError(err)
	}
	wildcard := w.proto(ctx)
	wildcard.Certificate = certificateState(w.recorded.Host, w.recorded.Host.Probe, nil, health.Status)
	renewalOf(wildcard, health)
	return &contractv1.GetPreviewWildcardResponse{
		Wildcard: wildcard,
		Projects: served,
	}, nil
}

func recordedPreviewWildcard(ctx context.Context, p provider.Provider) (*contractv1.PreviewWildcard, error) {
	recorded, err := stackrecords.ReadWildcard(ctx, p.KeyValues())
	if err != nil {
		return nil, err
	}
	if recorded.BaseDomain == "" {
		return nil, nil
	}
	w := &wildcards{provider: p, keyValues: p.KeyValues(), recorded: recorded}
	wildcard := w.proto(ctx)
	health, err := p.Certificates().Inspect(ctx, recorded.Edge, recorded.Hostname(), recorded.Host.Certificate)
	if err != nil {
		return nil, err
	}
	renewalOf(wildcard, health)
	return wildcard, nil
}

func renewalOf(wildcard *contractv1.PreviewWildcard, health provider.CertificateHealth) {
	wildcard.RenewalStatus = health.Renewal
	wildcard.ExpiresAt = health.ExpiresAt
	wildcard.ExpiringSoon = health.ExpiringSoon
}

func (w *wildcards) proto(ctx context.Context) *contractv1.PreviewWildcard {
	return &contractv1.PreviewWildcard{
		BaseDomain:     w.recorded.BaseDomain,
		EdgeScope:      w.recorded.Scope,
		GrammarMin:     w.recorded.GrammarMin,
		GrammarMax:     w.recorded.GrammarMax,
		RouteInstalled: w.routeInstalled(ctx),
	}
}

func (w *wildcards) routeInstalled(ctx context.Context) bool {
	if !w.recorded.IsRecorded() {
		return false
	}
	front, err := w.provider.Edges().Open(w.recorded.Edge)
	if err != nil {
		return false
	}
	owner, err := front.DomainOwner(ctx, w.recorded.Hostname())
	if err != nil {
		return false
	}
	return owner == edge.PreviewEntryOwner
}

func (w *wildcards) served(ctx context.Context) ([]string, error) {
	return stackrecords.ProjectsServedOnPreview(ctx, w.keyValues, w.recorded.BaseDomain)
}

func (w *wildcards) projectsWithLivePreviews(ctx context.Context) ([]string, error) {
	served, err := w.served(ctx)
	if err != nil {
		return nil, err
	}
	var live []string
	for _, slug := range served {
		environments, err := stackrecords.PreviewEnvironments(ctx, w.keyValues, slug)
		if err != nil {
			return nil, err
		}
		if len(environments) > 0 {
			live = append(live, slug)
		}
	}
	return live, nil
}

func (w *wildcards) refuseReleaseWhileLive(ctx context.Context) error {
	live, err := w.projectsWithLivePreviews(ctx)
	if err != nil {
		return err
	}
	if len(live) == 0 {
		return nil
	}
	return refusal.Refuse(refusal.CodeNotReady,
		"%s still has live preview pointers for %d project(s): %s — nothing would serve them the moment it is released. Remove one preview with `ocel preview rm`, or a project's whole preview footprint with `ocel destroy preview`, in each of them first",
		w.recorded.Hostname(), len(live), strings.Join(live, ", "))
}

func (w *wildcards) owningEdge() (edge.Edge, error) {
	return w.provider.Edges().Open(w.recorded.Edge)
}

func (w *wildcards) disclaimEntry(ctx context.Context, front edge.Edge, runProgress progress.Log) error {
	origin, err := w.entryOrigin(front)
	if err != nil || origin == nil {
		return err
	}
	if err := origin.DisclaimPreviewEntry(ctx, w.recorded.BaseDomain); err != nil {
		return err
	}
	revokeOriginCertificate(ctx, front, w.recorded.Host.OriginCertificateID, runProgress)
	return nil
}

func (h *handlers) PlanRemovePreviewWildcard(ctx context.Context, req *contractv1.PreviewWildcardRequest) (*planv1.ChangePlan, error) {
	w, err := h.wildcard(ctx, req.GetEdge())
	if err != nil {
		return nil, provider.RefusalError(err)
	}
	if w.recorded.BaseDomain == "" {
		return &planv1.ChangePlan{}, nil
	}
	front, err := w.owningEdge()
	if err != nil {
		return nil, provider.RefusalError(err)
	}
	if err := w.refuseReleaseWhileLive(ctx); err != nil {
		return nil, provider.RefusalError(err)
	}
	groups, err := w.releaseGroups(front)
	if err != nil {
		return nil, provider.RefusalError(err)
	}
	return &planv1.ChangePlan{
		EdgeKind: string(front.Kind()),
		Groups:   groups,
		Subject:  w.recorded.BaseDomain,
	}, nil
}

func (w *wildcards) releaseGroups(front edge.Edge) ([]*planv1.ChangeGroup, error) {
	removed, kept := front.PreviewWildcardRemovals(w.recorded.Hostname())
	removedGroup, err := edgeGroupProto(removed)
	if err != nil {
		return nil, err
	}
	groups := []*planv1.ChangeGroup{removedGroup}
	origin, err := w.entryOrigin(front)
	if err != nil {
		return nil, err
	}
	if origin != nil {
		for _, group := range origin.PlanPreviewEntryRemoval(w.recorded.Hostname()) {
			converted, err := edgeGroupProto(group)
			if err != nil {
				return nil, err
			}
			groups = append(groups, converted)
		}
	}
	for _, cert := range w.recorded.Host.Certificates() {
		groups = append(groups, certificateGroup(cert))
	}
	for _, rec := range w.recorded.Host.WrittenRecords() {
		groups = append(groups, &planv1.ChangeGroup{
			Kind:   "DNS record",
			Name:   rec.String(),
			Action: planv1.Change_ACTION_DELETE,
			Reason: "ocel wrote it; it is removed only while its live value is still the one ocel wrote",
		})
	}
	for _, rec := range w.recorded.Host.ManualRecords() {
		groups = append(groups, &planv1.ChangeGroup{
			Kind:   "DNS record",
			Name:   rec.String(),
			Action: planv1.Change_ACTION_KEEP,
			Reason: "you created it yourself; ocel never wrote it, so it is yours to remove",
		})
	}
	keptGroup, err := edgeGroupProto(kept)
	if err != nil {
		return nil, err
	}
	return append(groups, keptGroup), nil
}

func edgeGroupProto(group edge.PlanGroup) (*planv1.ChangeGroup, error) {
	converted, err := bootstrapplan.EdgeGroupFromPlanGroup(group)
	if err != nil {
		return nil, err
	}
	return GroupProto(converted), nil
}

func (h *handlers) RemovePreviewWildcard(ctx context.Context, req *contractv1.PreviewWildcardRequest, stream *connect.ServerStream[progressv1.OperationEvent]) error {
	root := RootSpan(naming.SpanEdge, string(environment.TierPreview), progress.Releasing.Title("the global preview domain"), progressv1.Phase_PHASE_DESTROY)
	return streamed(ctx, stream, root, func(_ *eventStream, progress progress.Log) error {
		w, err := h.wildcard(ctx, req.GetEdge())
		if err != nil {
			return err
		}
		return w.release(ctx, progress)
	})
}

func (w *wildcards) release(ctx context.Context, progress progress.Log) error {
	if w.recorded.BaseDomain == "" {
		progress.Say("Nothing to release: previews use no global preview domain")
		return nil
	}
	front, err := w.owningEdge()
	if err != nil {
		return err
	}
	if err := w.refuseReleaseWhileLive(ctx); err != nil {
		return err
	}
	cutover, err := w.dnsCutover(front)
	if err != nil {
		return err
	}
	progress.Say("Removing the shared preview entry on " + w.recorded.Hostname())
	if err := front.DestroyPreviewWildcard(ctx, w.recorded.BaseDomain); err != nil {
		return err
	}
	if err := w.disclaimEntry(ctx, front, progress); err != nil {
		return err
	}
	if err := cutover.release(ctx, w.recorded.Host.WrittenRecords(), progress.Say); err != nil {
		return err
	}
	for _, cert := range w.recorded.Host.Certificates() {
		if !cert.Requested {
			continue
		}
		progress.Say("Discarding certificate " + cert.ID)
		if err := discardCertificate(ctx, w.provider, cert, progress); err != nil {
			return err
		}
	}
	return keyvalue.Forget(ctx, w.keyValues, stackrecords.WildcardKey(environment.TierPreview))
}
