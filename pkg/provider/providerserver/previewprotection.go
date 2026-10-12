package providerserver

import (
	"context"
	"errors"
	"slices"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/edge/previewgate"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

const maxPreviewGateWrites = 3

var errPreviewGateUnpublished = errors.New("an edge did not take the preview gate")

var errPreviewGateIgnored = errors.New("a preview function ignores the preview gate")

func (h *handlers) GetPreviewProtection(ctx context.Context, req *contractv1.GetPreviewProtectionRequest) (*contractv1.GetPreviewProtectionResponse, error) {
	p, err := h.session.use()
	if err != nil {
		return nil, err
	}
	records, err := stackrecords.ReadPreviewGates(ctx, p.KeyValues(), p.Cipher())
	if err != nil {
		return nil, provider.RefusalError(err)
	}
	resp := &contractv1.GetPreviewProtectionResponse{}
	record, recorded := records[req.GetSlug()]
	if !recorded {
		return resp, nil
	}
	resp.Recorded, resp.Enabled = true, record.Enabled
	if req.GetRevealPassword() {
		resp.Password = record.Password
	}
	for _, secret := range record.Gate.BypassSecrets {
		resp.BypassSecretIds = append(resp.BypassSecretIds, deriveBypassSecretID(secret))
	}
	state, err := readPreviewEdgeState(ctx, p, req.GetSlug())
	if err != nil {
		return nil, provider.RefusalError(err)
	}
	if state.Kind == "" {
		return resp, nil
	}
	front := h.openPreviewEdge(p, state.Kind, req.GetEdge())
	status := describePreviewGateEdge(front, state.Kind, record)
	resp.Edges = []*progressv1.PreviewEdgePublication{status}
	if !record.Enabled || status.Error != "" {
		return resp, nil
	}
	if _, err := reportPreviewGateIgnored(ctx, p, front, req.GetSlug(), status); err != nil {
		return nil, provider.RefusalError(err)
	}
	return resp, nil
}

func (h *handlers) openPreviewEdge(p provider.Provider, kind edge.Kind, sel *contractv1.EdgeSelection) edge.Edge {
	if front, err := h.edgeFor(p, sel); err == nil && front.Kind() == kind {
		return front
	}
	if front, err := p.Edges().Open(kind, nil); err == nil {
		return front
	}
	return nil
}

func describePreviewGateEdge(front edge.Edge, kind edge.Kind, record stackrecords.PreviewGateRecord) *progressv1.PreviewEdgePublication {
	status := &progressv1.PreviewEdgePublication{Kind: string(kind), Published: record.Enabled && !record.Pending}
	if front != nil && !front.Facts().GatesPreviews {
		status.Published = false
		status.Error = describeFront(kind) + " runs no preview gate, so it answers the project's previews to anyone"
	}
	return status
}

func reportPreviewGateIgnored(ctx context.Context, p provider.Provider, front edge.Edge, slug string, status *progressv1.PreviewEdgePublication) (bool, error) {
	if front != nil && !front.Facts().GatesPreviewsAtOrigin {
		return false, nil
	}
	ignoring, err := listPreviewFunctionsIgnoringGate(ctx, p.KeyValues(), slug)
	if err != nil || len(ignoring) == 0 {
		return false, err
	}
	status.Published = false
	status.Error = describePreviewGateIgnored(ignoring)
	return true, nil
}

func readPreviewEdgeState(ctx context.Context, p provider.Provider, slug string) (stackrecords.EdgeState, error) {
	store := edgeStateStore{keyValues: p.KeyValues(), name: stackrecords.EdgeStackKey(environment.TierPreview, slug)}
	return store.read(ctx)
}

func (h *handlers) readPreviewFront(ctx context.Context, p provider.Provider, slug string, sel *contractv1.EdgeSelection) (edge.Edge, stackrecords.EdgeState, error) {
	state, err := readPreviewEdgeState(ctx, p, slug)
	if err != nil || state.Kind == "" {
		return nil, state, err
	}
	front, err := h.edgeFor(p, sel)
	if err != nil {
		return nil, state, err
	}
	if front.Kind() != state.Kind {
		return nil, state, refusal.Refuse(refusal.CodeInvalid,
			"the previews of %s are served through %s, and this run selected %s: select the edge that serves them",
			slug, describeFront(state.Kind), describeFront(front.Kind()))
	}
	return front, state, nil
}

func (h *handlers) ChangePreviewProtection(ctx context.Context, req *contractv1.ChangePreviewProtectionRequest) (*contractv1.ChangePreviewProtectionResponse, error) {
	p, err := h.session.use()
	if err != nil {
		return nil, err
	}
	slug := req.GetSlug()
	if _, _, err := h.readPreviewFront(ctx, p, slug, req.GetEdge()); err != nil {
		return nil, provider.RefusalError(err)
	}
	var resp *contractv1.ChangePreviewProtectionResponse
	err = stackrecords.ChangePreviewGates(ctx, p.KeyValues(), p.Cipher(), func(records map[string]stackrecords.PreviewGateRecord) error {
		resp = &contractv1.ChangePreviewProtectionResponse{}
		record, recorded := records[slug]
		switch {
		case !recorded:
			created, err := newPreviewGateRecord()
			if err != nil {
				return err
			}
			record = created
		case !record.Enabled:
			return refuseOpenedPreviews(slug)
		}
		changed, err := applyPreviewProtectionChange(record, req, resp)
		if err != nil {
			return err
		}
		changed.Pending = true
		records[slug] = changed
		return nil
	})
	if err != nil {
		return nil, provider.RefusalError(err)
	}
	return resp, nil
}

func refuseOpenedPreviews(slug string) error {
	return refusal.Refuse(refusal.CodeInvalid,
		"the last preview deploy of %s opened its previews because previews.protection was off: its credentials are kept, and a preview deploy with previews.protection on restores them before they can be changed or published",
		slug)
}

func applyPreviewProtectionChange(record stackrecords.PreviewGateRecord, req *contractv1.ChangePreviewProtectionRequest, resp *contractv1.ChangePreviewProtectionResponse) (stackrecords.PreviewGateRecord, error) {
	slug := req.GetSlug()
	record.Gate.BypassSecrets = slices.Clone(record.Gate.BypassSecrets)
	switch change := req.GetChange().(type) {
	case *contractv1.ChangePreviewProtectionRequest_RotatePassword:
		password, err := newPreviewPassword()
		if err != nil {
			return record, err
		}
		resp.Password = password
		return withPassword(record, password)
	case *contractv1.ChangePreviewProtectionRequest_SetPassword:
		return withPassword(record, change.SetPassword.GetPassword())
	case *contractv1.ChangePreviewProtectionRequest_CreateBypassSecret:
		if len(record.Gate.BypassSecrets) >= maxBypassSecrets {
			return record, refusal.Refuse(refusal.CodeInvalid, "%s already has %d bypass secrets, the most a preview gate holds: run `ocel preview protection bypass revoke` on one it no longer needs first", slug, maxBypassSecrets)
		}
		secret, err := newBypassSecret()
		if err != nil {
			return record, err
		}
		record.Gate.BypassSecrets = append(record.Gate.BypassSecrets, secret)
		resp.BypassSecret, resp.BypassSecretId = secret, deriveBypassSecretID(secret)
		return record, nil
	case *contractv1.ChangePreviewProtectionRequest_RevokeBypassSecret:
		id := change.RevokeBypassSecret.GetId()
		kept := slices.DeleteFunc(slices.Clone(record.Gate.BypassSecrets), func(secret string) bool { return deriveBypassSecretID(secret) == id })
		if len(kept) == len(record.Gate.BypassSecrets) {
			return record, refusal.Refuse(refusal.CodeInvalid, "%s has no bypass secret %s: `ocel preview protection bypass ls` lists the ones it has", slug, id)
		}
		record.Gate.BypassSecrets = kept
		return withPassword(record, record.Password)
	}
	return record, refusal.Refuse(refusal.CodeInvalid, "name a change to the preview protection of %s", slug)
}

func withPassword(record stackrecords.PreviewGateRecord, password string) (stackrecords.PreviewGateRecord, error) {
	key, err := newPreviewGateKey()
	if err != nil {
		return record, err
	}
	record.Password = password
	record.Gate.Key = key
	record.Gate.PasswordMAC = previewgate.HashPassword(key, password)
	return record, nil
}

func (h *handlers) PublishPreviewProtection(ctx context.Context, req *contractv1.PublishPreviewProtectionRequest, stream *connect.ServerStream[progressv1.OperationEvent]) error {
	p, err := h.session.use()
	if err != nil {
		return err
	}
	slug := req.GetSlug()
	root := RootSpan(naming.SpanEdge, slug, progress.Updating.Title("the preview protection of "+slug), progressv1.Phase_PHASE_PROVISION)
	return streamResult(ctx, stream, func(sender *eventStream) (*progressv1.OperationEvent, error) {
		outcome := &progressv1.PreviewProtectionPublished{}
		err := inSpan(sender, root, func(_ *eventStream, log progress.Log) error {
			return h.publishPreviewProtection(ctx, p, req, outcome, log)
		})
		switch {
		case errors.Is(err, errPreviewGateUnpublished):
			return failedPublication(outcome), nil
		case errors.Is(err, errPreviewGateIgnored):
			return ignoredPublication(outcome), nil
		case err != nil:
			return nil, err
		}
		return publication(outcome), nil
	})
}

func publication(outcome *progressv1.PreviewProtectionPublished) *progressv1.OperationEvent {
	return &progressv1.OperationEvent{
		Level: progressv1.Level_LEVEL_INFO,
		Body:  &progressv1.OperationEvent_Result{Result: &progressv1.OperationResult{Success: true, Protection: outcome}},
	}
}

func failedPublication(outcome *progressv1.PreviewProtectionPublished) *progressv1.OperationEvent {
	return &progressv1.OperationEvent{
		Level: progressv1.Level_LEVEL_ERROR,
		Body: &progressv1.OperationEvent_Result{Result: &progressv1.OperationResult{
			Error:      "the change is recorded, but not every edge took it: run `ocel preview protection sync` to publish it again",
			Protection: outcome,
		}},
	}
}

func ignoredPublication(outcome *progressv1.PreviewProtectionPublished) *progressv1.OperationEvent {
	return &progressv1.OperationEvent{
		Level: progressv1.Level_LEVEL_ERROR,
		Body: &progressv1.OperationEvent_Result{Result: &progressv1.OperationResult{
			Error:      "the change is published to every preview that checks the gate, but some preview functions ignore the preview gate and still answer anyone who has their URLs",
			Protection: outcome,
		}},
	}
}

func (h *handlers) publishPreviewProtection(ctx context.Context, p provider.Provider, req *contractv1.PublishPreviewProtectionRequest, outcome *progressv1.PreviewProtectionPublished, log progress.Log) error {
	slug := req.GetSlug()
	front, state, err := h.readPreviewFront(ctx, p, slug, req.GetEdge())
	if err != nil {
		return err
	}
	records, err := stackrecords.ReadPreviewGates(ctx, p.KeyValues(), p.Cipher())
	if err != nil {
		return err
	}
	record, recorded := records[slug]
	if !recorded {
		log.Say("No preview gate is recorded for " + slug + ", so there is nothing to publish")
		return nil
	}
	if !record.Enabled {
		return refuseOpenedPreviews(slug)
	}
	gate := &record.Gate
	if front == nil {
		log.Say("No preview of " + slug + " has deployed yet, so no edge holds a gate: the first preview deploy writes the recorded one")
		return nil
	}
	published := &progressv1.PreviewEdgePublication{Kind: string(front.Kind())}
	outcome.Edges = append(outcome.Edges, published)
	publish := previewGatePublisher(ctx, front, state.Edge, log)
	if publish == nil {
		published.Error = describeFront(front.Kind()) + " runs no preview gate, so it answers the previews of " + slug + " to anyone: serve them through an edge that gates previews"
		return errPreviewGateUnpublished
	}
	if err := publish(gate); err != nil {
		published.Error = err.Error()
		return errors.Join(errPreviewGateUnpublished, markPreviewGatePending(ctx, p, slug))
	}
	if err := settlePreviewGate(ctx, p, slug, gate, publish); err != nil {
		published.Error = err.Error()
		return errors.Join(errPreviewGateUnpublished, markPreviewGatePending(ctx, p, slug))
	}
	published.Published = true
	ignored, err := reportPreviewGateIgnored(ctx, p, front, slug, published)
	if err != nil {
		return err
	}
	if ignored {
		return errPreviewGateIgnored
	}
	return nil
}

func previewGatePublisher(ctx context.Context, front edge.Edge, state edge.StackState, log progress.Log) func(*previewgate.Gate) error {
	hook := front.Hooks().PublishPreviewGate
	if hook == nil || !front.Facts().GatesPreviews {
		return nil
	}
	return func(gate *previewgate.Gate) error {
		log.Say("Publishing the preview gate of " + state.Slug + " to " + describeFront(front.Kind()))
		return hook(ctx, edge.PreviewGatePublication{State: state, Gate: gate, Say: log.Say})
	}
}

func settlePreviewGate(ctx context.Context, p provider.Provider, slug string, written *previewgate.Gate, publish func(*previewgate.Gate) error) error {
	for range maxPreviewGateWrites {
		var current *previewgate.Gate
		settled := false
		err := stackrecords.ChangePreviewGates(ctx, p.KeyValues(), p.Cipher(), func(records map[string]stackrecords.PreviewGateRecord) error {
			record, recorded := records[slug]
			current = record.GateInForce()
			settled = samePreviewGate(current, written)
			if recorded {
				record.Pending = !settled
				records[slug] = record
			}
			return nil
		})
		if err != nil || settled {
			return err
		}
		if publish == nil {
			return errPreviewGateUnpublished
		}
		if err := publish(current); err != nil {
			return err
		}
		written = current
	}
	return errPreviewGateUnpublished
}

func markPreviewGatePending(ctx context.Context, p provider.Provider, slug string) error {
	return stackrecords.ChangePreviewGates(ctx, p.KeyValues(), p.Cipher(), func(records map[string]stackrecords.PreviewGateRecord) error {
		if record, recorded := records[slug]; recorded {
			record.Pending = true
			records[slug] = record
		}
		return nil
	})
}
