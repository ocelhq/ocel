package providerkit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	connect "connectrpc.com/connect"

	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type wildcards struct {
	provider Provider
	records  RecordStore
	held     Wildcard
	writer   edge.DNSWriter
	zone     string
	ask      func(headline string, records []edge.Record, notes ...string)
}

func (h *handlers) wildcard(ctx context.Context, sel *contractv1.EdgeSelection) (*wildcards, error) {
	provider, err := h.session.use()
	if err != nil {
		return nil, err
	}
	records := provider.Records()
	held, err := readWildcard(ctx, records)
	if err != nil {
		return nil, err
	}
	writer, err := h.dnsFor(provider, sel)
	if err != nil {
		return nil, err
	}
	return &wildcards{
		provider: provider,
		records:  records,
		held:     held,
		writer:   writer,
		zone:     sel.GetDns().GetZone(),
	}, nil
}

func (w *wildcards) settler(front edge.Edge) settler {
	s := newSettler(front, w.writer, w.zone, servedResolver{kind: front.Kind()})
	s.ask = w.ask
	return s
}

type servedResolver struct{ kind edge.Kind }

func (r servedResolver) Serving(context.Context, string) (edge.Kind, error) { return r.kind, nil }

func readWildcard(ctx context.Context, records RecordStore) (Wildcard, error) {
	name := WildcardRecord(ClassPreview)
	record, err := Held(ctx, records, name)
	if err != nil {
		return Wildcard{}, fmt.Errorf("read %s: %w", name, err)
	}
	var held Wildcard
	if len(record.Bytes) == 0 {
		return held, nil
	}
	if err := json.Unmarshal(record.Bytes, &held); err != nil {
		return Wildcard{}, fmt.Errorf("read %s: %w", name, err)
	}
	return held, nil
}

func (w *wildcards) save(ctx context.Context) error {
	name := WildcardRecord(ClassPreview)
	record, err := Held(ctx, w.records, name)
	if err != nil {
		return fmt.Errorf("read %s: %w", name, err)
	}
	if record.Bytes, err = json.Marshal(w.held); err != nil {
		return fmt.Errorf("record %s: %w", name, err)
	}
	if _, err := w.records.Write(ctx, record); err != nil {
		return fmt.Errorf("record %s: %w", name, err)
	}
	return nil
}

func (h *handlers) UsePreviewWildcard(ctx context.Context, req *contractv1.UsePreviewWildcardRequest, stream *connect.ServerStream[progressv1.OperationEvent]) error {
	return streamed(ctx, stream, progressv1.Phase_PHASE_UNSPECIFIED, func(sender *eventSender, report Reporter) error {
		base, err := previewBaseDomain(req.GetBaseDomain())
		if err != nil {
			return err
		}
		w, err := h.wildcard(ctx, req.GetEdge())
		if err != nil {
			return err
		}
		w.ask = func(headline string, records []edge.Record, notes ...string) {
			sender.send(dnsOwedEvent(headline, records, notes...))
		}
		front, err := h.edgeFor(w.provider, req.GetEdge())
		if err != nil {
			return err
		}
		return w.use(ctx, front, base, report)
	})
}

func (w *wildcards) use(ctx context.Context, front edge.Edge, base string, report Reporter) error {
	if err := w.claimable(front, base); err != nil {
		return err
	}
	settle := w.settler(front)
	wildcard := edge.PreviewWildcard(base)
	w.held = Wildcard{
		BaseDomain: base,
		Edge:       front.Kind(),
		Scope:      front.Facts().CredentialScope,
		GrammarMin: edge.PreviewGrammarMin,
		GrammarMax: edge.PreviewGrammarMax,
		Settled:    w.held.Settled,
	}

	certifying := w.certification(settle, fmt.Sprintf(
		"If this run gives up waiting, re-run `ocel domain use '%s' --preview`.", wildcard))
	if err := certifying.certify(ctx, wildcard, report); err != nil {
		return err
	}

	report.Say("Reconciling the shared preview entry on " + wildcard)
	published, err := front.ReconcilePreviewWildcard(ctx, edge.PreviewWildcardSpec{
		BaseDomain:  base,
		Certificate: w.held.Settled.Certificate.ID,
		GrammarMin:  edge.PreviewGrammarMin,
		GrammarMax:  edge.PreviewGrammarMax,
		Warn:        report.Detail,
	})
	if err != nil {
		return err
	}
	if err := w.save(ctx); err != nil {
		return err
	}
	if err := certifying.discardSuperseded(ctx, report); err != nil {
		return err
	}

	target := edge.DNSTarget{Kind: front.Kind(), ServesUnbound: front.Facts().ServesUnbound, Front: published}
	records, err := edge.RecordsFor(target, []string{wildcard})
	if err != nil {
		return err
	}
	written, werr := settle.write(ctx, records,
		fmt.Sprintf("Point %s at the %s edge", wildcard, front.Kind()), report.Say,
		fmt.Sprintf("If this run gives up waiting, re-run `ocel domain use '%s' --preview`.", wildcard))
	w.held.Settled.Written, w.held.Settled.Owed = written.Written, written.Owed
	if err := w.save(ctx); err != nil {
		return errors.Join(werr, err)
	}
	if werr != nil {
		return werr
	}

	probe, aerr := settle.await(ctx, wildcard, report.Say)
	w.held.Settled.Probe = probe
	if err := w.save(ctx); err != nil {
		return errors.Join(aerr, err)
	}
	if aerr != nil {
		return aerr
	}
	report.Say(fmt.Sprintf("Previews are served on %s by the %s edge", wildcard, front.Kind()))
	return nil
}

func (w *wildcards) certification(settle settler, notes ...string) certification {
	return certification{
		provider: w.provider,
		settle:   settle,
		settled:  &w.held.Settled,
		persist:  w.save,
		notes:    notes,
	}
}

func (w *wildcards) claimable(front edge.Edge, base string) error {
	if w.held.BaseDomain != "" && w.held.BaseDomain != base {
		return Refuse(CodeNotReady,
			"this preview bootstrap already serves previews on %q: release it with `ocel domain release --preview` first, then use %q — every project on %q loses its preview hostnames the moment the bootstrap changes domain, so that is two deliberate commands",
			w.held.BaseDomain, base, w.held.BaseDomain)
	}
	holder, held := w.held.Holder()
	if !held || holder == front.Kind() {
		return nil
	}
	return Refuse(CodeNotReady,
		"%s is already held by the %s edge, and this project's edge is %s: reconciling it here would raise a second wildcard at the %s edge and leave the %s one standing with nothing left to name it — release it with `ocel domain release --preview` from a project on the %s edge first, then use it again from here",
		w.held.Hostname(), holder, front.Kind(), front.Kind(), holder, holder)
}

func (h *handlers) GetPreviewWildcard(ctx context.Context, req *contractv1.PreviewWildcardRequest) (*contractv1.GetPreviewWildcardResponse, error) {
	w, err := h.wildcard(ctx, req.GetEdge())
	if err != nil {
		return nil, RefusalError(err)
	}
	if w.held.BaseDomain == "" {
		return &contractv1.GetPreviewWildcardResponse{}, nil
	}
	served, err := w.served(ctx)
	if err != nil {
		return nil, RefusalError(err)
	}
	health, err := inspectCertificate(ctx, w.provider, w.held.Edge, w.held.Hostname(), w.held.Settled.Certificate)
	if err != nil {
		return nil, RefusalError(err)
	}
	return &contractv1.GetPreviewWildcardResponse{
		Wildcard: &contractv1.PreviewWildcard{
			BaseDomain:     w.held.BaseDomain,
			EdgeScope:      w.held.Scope,
			GrammarMin:     w.held.GrammarMin,
			GrammarMax:     w.held.GrammarMax,
			RouteInstalled: w.routeInstalled(ctx),
			Certificate:    certificateState(w.held.Settled, w.held.Settled.Probe, nil, health.Status),
		},
		Projects: served,
	}, nil
}

func (w *wildcards) routeInstalled(ctx context.Context) bool {
	holder, held := w.held.Holder()
	if !held {
		return false
	}
	front, err := w.provider.Edges().Open(holder)
	if err != nil {
		return false
	}
	owner, err := front.DomainOwner(ctx, w.held.Hostname())
	if err != nil {
		return false
	}
	return owner == edge.PreviewEntryOwner
}

func (w *wildcards) served(ctx context.Context) ([]string, error) {
	under := EdgeStacksRecord(ClassPreview)
	held, err := w.records.List(ctx, under)
	if err != nil {
		return nil, fmt.Errorf("read the projects served on %s: %w", w.held.Hostname(), err)
	}
	var served []string
	for _, record := range held {
		rest, named := record.Name.Under(under)
		if !named || len(record.Bytes) == 0 {
			continue
		}
		var state EdgeStackState
		if err := json.Unmarshal(record.Bytes, &state); err != nil {
			return nil, fmt.Errorf("read %s: %w", record.Name, err)
		}
		if state.Edge.ServedOnGlobalPreview(w.held.BaseDomain) {
			served = append(served, rest[0])
		}
	}
	slices.Sort(served)
	return served, nil
}

func (w *wildcards) live(ctx context.Context) ([]string, error) {
	served, err := w.served(ctx)
	if err != nil {
		return nil, err
	}
	var carrying []string
	for _, slug := range served {
		environments, err := previewEnvironments(ctx, w.records, slug)
		if err != nil {
			return nil, err
		}
		if len(environments) > 0 {
			carrying = append(carrying, slug)
		}
	}
	return carrying, nil
}

func (w *wildcards) releasable(ctx context.Context) error {
	carrying, err := w.live(ctx)
	if err != nil {
		return err
	}
	if len(carrying) == 0 {
		return nil
	}
	return Refuse(CodeNotReady,
		"%s still carries live preview pointers for %d project(s): %s — nothing would serve them the moment it is released. Remove one preview with `ocel preview rm`, or a project's whole preview footprint with `ocel destroy --preview`, in each of them first",
		w.held.Hostname(), len(carrying), strings.Join(carrying, ", "))
}

func (w *wildcards) holding() (edge.Edge, error) {
	holder, held := w.held.Holder()
	if !held {
		return nil, Refuse(CodeNotReady,
			"nothing in this account records which edge holds %s, and tearing it down through a guessed edge would delete its certificate, its DNS records and the record itself while leaving the real wildcard entry standing with nothing left to name it: run `ocel domain use '%s' --preview` from the project whose edge raised it — that writes the edge down and changes nothing else — then release it",
			w.held.Hostname(), w.held.Hostname())
	}
	return w.provider.Edges().Open(holder)
}

func (h *handlers) PlanRemovePreviewWildcard(ctx context.Context, req *contractv1.PreviewWildcardRequest) (*contractv1.RemovalPlan, error) {
	w, err := h.wildcard(ctx, req.GetEdge())
	if err != nil {
		return nil, RefusalError(err)
	}
	if w.held.BaseDomain == "" {
		return &contractv1.RemovalPlan{}, nil
	}
	front, err := w.holding()
	if err != nil {
		return nil, RefusalError(err)
	}
	if err := w.releasable(ctx); err != nil {
		return nil, RefusalError(err)
	}
	return &contractv1.RemovalPlan{
		EdgeKind: string(front.Kind()),
		Items:    w.releaseItems(front),
		Subject:  w.held.BaseDomain,
	}, nil
}

func (w *wildcards) releaseItems(front edge.Edge) []*contractv1.RemovalItem {
	removed, kept := front.PreviewWildcardSurfaces(w.held.Hostname())
	items := []*contractv1.RemovalItem{surfaceItem(removed)}
	for _, cert := range w.held.Settled.certificates() {
		items = append(items, certificateItem(cert))
	}
	for _, rec := range w.held.Settled.WrittenRecords() {
		items = append(items, &contractv1.RemovalItem{
			Kind:   "DNS record",
			Name:   rec.String(),
			Action: contractv1.RemovalItem_ACTION_DELETE,
			Reason: "ocel wrote it; it is removed only while its live value is still the one ocel wrote",
		})
	}
	for _, rec := range w.held.Settled.OwedRecords() {
		items = append(items, &contractv1.RemovalItem{
			Kind:   "DNS record",
			Name:   rec.String(),
			Action: contractv1.RemovalItem_ACTION_KEEP,
			Reason: "you created it yourself; ocel never wrote it, so it is yours to remove",
		})
	}
	return append(items, surfaceItem(kept))
}

func surfaceItem(surface Removal) *contractv1.RemovalItem {
	return &contractv1.RemovalItem{
		Kind:   surface.Kind,
		Name:   surface.Name,
		Action: removalAction(surface.Action),
		Reason: surface.Reason,
		Slow:   surface.Slow,
	}
}

func (h *handlers) RemovePreviewWildcard(ctx context.Context, req *contractv1.PreviewWildcardRequest, stream *connect.ServerStream[progressv1.OperationEvent]) error {
	return streamed(ctx, stream, progressv1.Phase_PHASE_UNSPECIFIED, func(_ *eventSender, report Reporter) error {
		w, err := h.wildcard(ctx, req.GetEdge())
		if err != nil {
			return err
		}
		return w.release(ctx, report)
	})
}

func (w *wildcards) release(ctx context.Context, report Reporter) error {
	if w.held.BaseDomain == "" {
		report.Say("This preview bootstrap has no global preview domain")
		return nil
	}
	front, err := w.holding()
	if err != nil {
		return err
	}
	if err := w.releasable(ctx); err != nil {
		return err
	}
	report.Say("Removing the shared preview entry on " + w.held.Hostname())
	if err := front.DestroyPreviewWildcard(ctx, w.held.BaseDomain); err != nil {
		return err
	}
	if err := w.settler(front).release(ctx, w.held.Settled.WrittenRecords(), report.Say); err != nil {
		return err
	}
	for _, cert := range w.held.Settled.certificates() {
		if !cert.Requested {
			continue
		}
		report.Say("Discarding certificate " + cert.ID)
		if err := discardCertificate(ctx, w.provider, cert, report); err != nil {
			return err
		}
	}
	return Forget(ctx, w.records, WildcardRecord(ClassPreview))
}
