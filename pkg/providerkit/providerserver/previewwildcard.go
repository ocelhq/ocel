package providerserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/naming"
	planv1 "github.com/ocelhq/ocel/pkg/proto/common/plan/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/providerkit/bootstrapplan"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/records"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	"github.com/ocelhq/ocel/pkg/providerkit/stackrecords"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type wildcards struct {
	provider       provider.Provider
	records        records.Store
	recorded       stackrecords.Wildcard
	sel            *contractv1.EdgeSelection
	progressStream *eventStream
}

func (h *handlers) wildcard(ctx context.Context, sel *contractv1.EdgeSelection) (*wildcards, error) {
	vendor, err := h.session.use()
	if err != nil {
		return nil, err
	}
	store := vendor.Records()
	wildcard, err := stackrecords.ReadWildcard(ctx, store)
	if err != nil {
		return nil, err
	}
	return &wildcards{provider: vendor, records: store, recorded: wildcard, sel: sel}, nil
}

func (w *wildcards) dnsCutover(front edge.Edge) (dnsCutover, error) {
	writer, err := dnsFor(w.provider, front, w.sel)
	if err != nil {
		return dnsCutover{}, err
	}
	s := newDNSCutover(front, writer, w.sel.GetDns().GetZone(), w.provider.Liveness())
	if w.progressStream != nil {
		s.waitForManualRecords(w.progressStream)
	}
	return s, nil
}

func (w *wildcards) save(ctx context.Context) error {
	name := stackrecords.WildcardRecord(edge.ClassPreview)
	record, err := records.ReadOrEmpty(ctx, w.records, name)
	if err != nil {
		return fmt.Errorf("read %s: %w", name, err)
	}
	if record.Bytes, err = json.Marshal(w.recorded); err != nil {
		return fmt.Errorf("record %s: %w", name, err)
	}
	if _, err := w.records.Write(ctx, record); err != nil {
		return fmt.Errorf("record %s: %w", name, err)
	}
	return nil
}

func (h *handlers) UsePreviewWildcard(ctx context.Context, req *contractv1.UsePreviewWildcardRequest, stream *connect.ServerStream[progressv1.OperationEvent]) error {
	return streamed(ctx, stream, naming.UnitEdge, edgeUnitTitle, progressv1.Phase_PHASE_PROVISIONING, func(sender *eventStream, progress edge.Progress) error {
		base, err := previewBaseDomain(req.GetBaseDomain())
		if err != nil {
			return err
		}
		w, err := h.wildcard(ctx, req.GetEdge())
		if err != nil {
			return err
		}
		w.progressStream = sender
		front, err := h.edgeFor(w.provider, req.GetEdge())
		if err != nil {
			return err
		}
		return w.use(ctx, front, base, progress)
	})
}

func (w *wildcards) use(ctx context.Context, front edge.Edge, base string, progress edge.Progress) error {
	if err := w.claimable(front, base); err != nil {
		return err
	}
	cutover, err := w.dnsCutover(front)
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

	progress.Say("Reconciling the shared preview entry on " + wildcard)
	program, err := edgeProgramFor(ctx, w.provider, front, provider.EdgeProgramRequest{
		Class:             edge.ClassPreview,
		PreviewBaseDomain: base,
	})
	if err != nil {
		return err
	}
	published, err := front.ReconcilePreviewWildcard(ctx, edge.PreviewWildcardSpec{
		BaseDomain:  base,
		Certificate: w.recorded.Host.Certificate.ID,
		GrammarMin:  edge.PreviewGrammarMin,
		GrammarMax:  edge.PreviewGrammarMax,
		Warn:        progress.Detail,
		Program:     program.Spec,
		Values:      program.Values,
	})
	if err != nil {
		return err
	}
	if err := w.save(ctx); err != nil {
		return err
	}
	if err := certifying.discardSuperseded(ctx, progress); err != nil {
		return err
	}

	target := edge.DNSTarget{Kind: front.Kind(), ServesUnbound: front.Facts().ServesUnbound, Front: published}
	dnsRecords, err := edge.RecordsFor(target, []string{wildcard})
	if err != nil {
		return err
	}
	written, werr := cutover.write(ctx, dnsRecords,
		fmt.Sprintf("Point %s at the %s edge", wildcard, front.Kind()), progress.Say,
		fmt.Sprintf("If this run gives up waiting, re-run `ocel domain use '%s' --preview`.", wildcard))
	w.recorded.Host.Written, w.recorded.Host.Manual = written.Written, written.Manual
	if err := w.save(ctx); err != nil {
		return errors.Join(werr, err)
	}
	if werr != nil {
		return werr
	}

	probe, aerr := cutover.await(ctx, wildcard, progress.Say)
	w.recorded.Host.Probe = probe
	if err := w.save(ctx); err != nil {
		return errors.Join(aerr, err)
	}
	if aerr != nil {
		return aerr
	}
	progress.Say(fmt.Sprintf("Previews are served on %s by the %s edge", wildcard, front.Kind()))
	return nil
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
	owningEdge, owned := w.recorded.OwningEdge()
	if !owned || owningEdge == front.Kind() {
		return nil
	}
	return refusal.Refuse(refusal.CodeNotReady,
		"%s is already owned by the %s edge, and this project's edge is %s: reconciling it here would raise a second wildcard at the %s edge and leave the %s one in place with nothing left to name it — release it with `ocel domain release --preview` from a project on the %s edge first, then use it again from here",
		w.recorded.Hostname(), owningEdge, front.Kind(), front.Kind(), owningEdge, owningEdge)
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
	recorded, err := stackrecords.ReadWildcard(ctx, p.Records())
	if err != nil {
		return nil, err
	}
	if recorded.BaseDomain == "" {
		return nil, nil
	}
	w := &wildcards{provider: p, records: p.Records(), recorded: recorded}
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
	owningEdge, owned := w.recorded.OwningEdge()
	if !owned {
		return false
	}
	front, err := w.provider.Edges().Open(owningEdge)
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
	return stackrecords.ProjectsServedOnPreview(ctx, w.records, w.recorded.BaseDomain)
}

func (w *wildcards) projectsWithLivePreviews(ctx context.Context) ([]string, error) {
	served, err := w.served(ctx)
	if err != nil {
		return nil, err
	}
	var live []string
	for _, slug := range served {
		environments, err := stackrecords.PreviewEnvironments(ctx, w.records, slug)
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
	owningEdge, owned := w.recorded.OwningEdge()
	if !owned {
		return nil, refusal.Refuse(refusal.CodeNotReady,
			"nothing in this account records which edge owns %s, and tearing it down through a guessed edge would delete its certificate, its DNS records and the record itself while leaving the real wildcard entry in place with nothing left to name it: run `ocel domain use '%s' --preview` from the project whose edge raised it — that writes the edge down and changes nothing else — then release it",
			w.recorded.Hostname(), w.recorded.Hostname())
	}
	return w.provider.Edges().Open(owningEdge)
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
	return streamed(ctx, stream, naming.UnitEdge, edgeUnitTitle, progressv1.Phase_PHASE_DELETING, func(_ *eventStream, progress edge.Progress) error {
		w, err := h.wildcard(ctx, req.GetEdge())
		if err != nil {
			return err
		}
		return w.release(ctx, progress)
	})
}

func (w *wildcards) release(ctx context.Context, progress edge.Progress) error {
	if w.recorded.BaseDomain == "" {
		progress.Say("This preview bootstrap has no global preview domain")
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
	return records.Forget(ctx, w.records, stackrecords.WildcardRecord(edge.ClassPreview))
}
