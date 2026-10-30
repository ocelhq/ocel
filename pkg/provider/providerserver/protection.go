package providerserver

import (
	"context"
	"errors"
	"slices"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/edge/gate"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

const maxGateWrites = 3

var errGateUnpublished = errors.New("an edge did not take the gate")

var errGateIgnored = errors.New("a function ignores the gate")

func (h *handlers) GetProtection(ctx context.Context, req *contractv1.GetProtectionRequest) (*contractv1.GetProtectionResponse, error) {
	p, err := h.session.use()
	if err != nil {
		return nil, err
	}
	tier, err := decodeRequiredTier(req.GetTier())
	if err != nil {
		return nil, err
	}
	records, err := stackrecords.ReadGates(ctx, p.KeyValues(), p.Cipher(), tier)
	if err != nil {
		return nil, provider.RefusalError(err)
	}
	resp := &contractv1.GetProtectionResponse{}
	record, recorded := records[req.GetSlug()]
	if !recorded {
		return resp, nil
	}
	resp.Recorded, resp.Enabled = true, record.Enabled
	for _, secret := range record.Gate.BypassSecrets {
		resp.BypassSecretIds = append(resp.BypassSecretIds, deriveBypassSecretID(secret))
	}
	state, err := readEdgeState(ctx, p, tier, req.GetSlug())
	if err != nil {
		return nil, provider.RefusalError(err)
	}
	if state.Kind == "" {
		return resp, nil
	}
	front := h.openEdge(p, state.Kind, req.GetEdge())
	status := describeGateEdge(front, tier, req.GetSlug(), state.Kind, record)
	resp.Edges = []*progressv1.EdgePublication{status}
	if !record.Enabled || status.Error != "" {
		return resp, nil
	}
	if _, err := reportGateIgnored(ctx, p, front, tier, req.GetSlug(), status); err != nil {
		return nil, provider.RefusalError(err)
	}
	return resp, nil
}

func (h *handlers) RevealProtectionPassword(ctx context.Context, req *contractv1.RevealProtectionPasswordRequest) (*contractv1.RevealProtectionPasswordResponse, error) {
	p, err := h.session.use()
	if err != nil {
		return nil, err
	}
	tier, err := decodeRequiredTier(req.GetTier())
	if err != nil {
		return nil, err
	}
	records, err := stackrecords.ReadGates(ctx, p.KeyValues(), p.Cipher(), tier)
	if err != nil {
		return nil, provider.RefusalError(err)
	}
	record, recorded := records[req.GetSlug()]
	if !recorded {
		return &contractv1.RevealProtectionPasswordResponse{}, nil
	}
	return &contractv1.RevealProtectionPasswordResponse{Recorded: true, Password: record.Password}, nil
}

func (h *handlers) openEdge(p provider.Provider, kind edge.Kind, sel *contractv1.EdgeSelection) edge.Edge {
	if front, err := h.edgeFor(p, sel); err == nil && front.Kind() == kind {
		return front
	}
	if front, err := p.Edges().Open(kind, nil); err == nil {
		return front
	}
	return nil
}

func describeGateEdge(front edge.Edge, tier environment.Tier, slug string, kind edge.Kind, record stackrecords.GateRecord) *progressv1.EdgePublication {
	status := &progressv1.EdgePublication{Kind: string(kind), Published: record.Enabled && !record.Pending}
	if front != nil && !front.Facts().Gates {
		status.Published = false
		status.Error = describeFront(kind) + " runs no gate, so it answers " + describeProtected(tier, slug) + " to anyone"
	}
	return status
}

func reportGateIgnored(ctx context.Context, p provider.Provider, front edge.Edge, tier environment.Tier, slug string, status *progressv1.EdgePublication) (bool, error) {
	if front != nil && !front.Facts().GatesAtOrigin {
		return false, nil
	}
	ignoring, err := listFunctionsIgnoringGate(ctx, p.KeyValues(), tier, slug)
	if err != nil || len(ignoring) == 0 {
		return false, err
	}
	status.Published = false
	status.Error = describeGateIgnored(tier, ignoring)
	return true, nil
}

func readEdgeState(ctx context.Context, p provider.Provider, tier environment.Tier, slug string) (stackrecords.EdgeState, error) {
	store := edgeStateStore{keyValues: p.KeyValues(), name: stackrecords.EdgeStackKey(tier, slug)}
	return store.read(ctx)
}

func (h *handlers) readFront(ctx context.Context, p provider.Provider, tier environment.Tier, slug string, sel *contractv1.EdgeSelection) (edge.Edge, stackrecords.EdgeState, error) {
	state, err := readEdgeState(ctx, p, tier, slug)
	if err != nil || state.Kind == "" {
		return nil, state, err
	}
	front, err := h.edgeFor(p, sel)
	if err != nil {
		return nil, state, err
	}
	if front.Kind() != state.Kind {
		return nil, state, refusal.Refuse(refusal.CodeInvalid,
			"the edge that serves %s is %s, and this run selected %s: select that edge",
			describeProtected(tier, slug), describeFront(state.Kind), describeFront(front.Kind()))
	}
	return front, state, nil
}

func (h *handlers) ChangeProtection(ctx context.Context, req *contractv1.ChangeProtectionRequest) (*contractv1.ChangeProtectionResponse, error) {
	p, err := h.session.use()
	if err != nil {
		return nil, err
	}
	tier, err := decodeRequiredTier(req.GetTier())
	if err != nil {
		return nil, err
	}
	slug := req.GetSlug()
	if _, _, err := h.readFront(ctx, p, tier, slug, req.GetEdge()); err != nil {
		return nil, provider.RefusalError(err)
	}
	var resp *contractv1.ChangeProtectionResponse
	err = stackrecords.ChangeGates(ctx, p.KeyValues(), p.Cipher(), tier, func(records map[string]stackrecords.GateRecord) error {
		resp = &contractv1.ChangeProtectionResponse{}
		record, recorded := records[slug]
		switch {
		case !recorded:
			created, err := newGateRecord()
			if err != nil {
				return err
			}
			record = created
		case !record.Enabled:
			return refuseOpened(tier, slug)
		}
		changed, err := applyChange(record, tier, req, resp)
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

func refuseOpened(tier environment.Tier, slug string) error {
	return refusal.Refuse(refusal.CodeInvalid,
		"the last %s deploy of %s opened %s because %s was off: the credentials are kept, and a %s deploy with %s on restores them before they can be changed or published",
		tier, slug, describeProtected(tier, slug), protectionKey(tier), tier, protectionKey(tier))
}

func applyChange(record stackrecords.GateRecord, tier environment.Tier, req *contractv1.ChangeProtectionRequest, resp *contractv1.ChangeProtectionResponse) (stackrecords.GateRecord, error) {
	slug := req.GetSlug()
	record.Gate.BypassSecrets = slices.Clone(record.Gate.BypassSecrets)
	switch change := req.GetChange().(type) {
	case *contractv1.ChangeProtectionRequest_RotatePassword:
		password, err := newPassword()
		if err != nil {
			return record, err
		}
		resp.Password = password
		return withPassword(record, password)
	case *contractv1.ChangeProtectionRequest_SetPassword:
		return withPassword(record, change.SetPassword.GetPassword())
	case *contractv1.ChangeProtectionRequest_CreateBypassSecret:
		if len(record.Gate.BypassSecrets) >= maxBypassSecrets {
			return record, refusal.Refuse(refusal.CodeInvalid, "%s already has %d %s bypass secrets, the most a gate holds: run `ocel protection bypass revoke%s` on one it no longer needs first", slug, maxBypassSecrets, tier, tierFlag(tier))
		}
		secret, err := newBypassSecret()
		if err != nil {
			return record, err
		}
		record.Gate.BypassSecrets = append(record.Gate.BypassSecrets, secret)
		resp.BypassSecret, resp.BypassSecretId = secret, deriveBypassSecretID(secret)
		return record, nil
	case *contractv1.ChangeProtectionRequest_RevokeBypassSecret:
		id := change.RevokeBypassSecret.GetId()
		kept := slices.DeleteFunc(slices.Clone(record.Gate.BypassSecrets), func(secret string) bool { return deriveBypassSecretID(secret) == id })
		if len(kept) == len(record.Gate.BypassSecrets) {
			return record, refusal.Refuse(refusal.CodeInvalid, "%s has no %s bypass secret %s: `ocel protection bypass ls%s` lists the ones it has", slug, tier, id, tierFlag(tier))
		}
		record.Gate.BypassSecrets = kept
		return withPassword(record, record.Password)
	}
	return record, refusal.Refuse(refusal.CodeInvalid, "name a change to the %s protection of %s", tier, slug)
}

func withPassword(record stackrecords.GateRecord, password string) (stackrecords.GateRecord, error) {
	key, err := newGateKey()
	if err != nil {
		return record, err
	}
	record.Password = password
	record.Gate.Key = key
	record.Gate.PasswordHash = gate.HashPassword(password)
	return record, nil
}

func (h *handlers) PublishProtection(ctx context.Context, req *contractv1.PublishProtectionRequest, stream *connect.ServerStream[progressv1.OperationEvent]) error {
	p, err := h.session.use()
	if err != nil {
		return err
	}
	tier, err := decodeRequiredTier(req.GetTier())
	if err != nil {
		return err
	}
	slug := req.GetSlug()
	root := RootSpan(SpanEdge, slug, progress.Updating.Title("the "+string(tier)+" protection of "+slug), progressv1.Phase_PHASE_PROVISION)
	return streamResult(ctx, stream, func(sender *eventStream) (*progressv1.OperationEvent, error) {
		outcome := &progressv1.ProtectionPublished{}
		err := inSpan(ctx, sender, root, func(ctx context.Context, _ *eventStream, log progress.Log) error {
			return h.publishProtection(ctx, p, tier, req, outcome, log)
		})
		switch {
		case errors.Is(err, errGateUnpublished):
			return failedPublication(tier, outcome), nil
		case errors.Is(err, errGateIgnored):
			return ignoredPublication(outcome), nil
		case err != nil:
			return nil, err
		}
		return publication(outcome), nil
	})
}

func publication(outcome *progressv1.ProtectionPublished) *progressv1.OperationEvent {
	return &progressv1.OperationEvent{
		Level: progressv1.Level_LEVEL_INFO,
		Body:  &progressv1.OperationEvent_Result{Result: &progressv1.OperationResult{Success: true, Protection: outcome}},
	}
}

func failedPublication(tier environment.Tier, outcome *progressv1.ProtectionPublished) *progressv1.OperationEvent {
	return &progressv1.OperationEvent{
		Level: progressv1.Level_LEVEL_ERROR,
		Body: &progressv1.OperationEvent_Result{Result: &progressv1.OperationResult{
			Error:      "the change is recorded, but not every edge took it: run `ocel protection sync" + tierFlag(tier) + "` to publish it again",
			Protection: outcome,
		}},
	}
}

func ignoredPublication(outcome *progressv1.ProtectionPublished) *progressv1.OperationEvent {
	return &progressv1.OperationEvent{
		Level: progressv1.Level_LEVEL_ERROR,
		Body: &progressv1.OperationEvent_Result{Result: &progressv1.OperationResult{
			Error:      "the change is published to everything that checks the gate, but some functions ignore the gate and still answer anyone who has their URLs",
			Protection: outcome,
		}},
	}
}

func (h *handlers) publishProtection(ctx context.Context, p provider.Provider, tier environment.Tier, req *contractv1.PublishProtectionRequest, outcome *progressv1.ProtectionPublished, log progress.Log) error {
	slug := req.GetSlug()
	front, state, err := h.readFront(ctx, p, tier, slug, req.GetEdge())
	if err != nil {
		return err
	}
	records, err := stackrecords.ReadGates(ctx, p.KeyValues(), p.Cipher(), tier)
	if err != nil {
		return err
	}
	record, recorded := records[slug]
	if !recorded {
		log.Say("No " + string(tier) + " gate is recorded for " + slug + ", so there is nothing to publish")
		return nil
	}
	if !record.Enabled {
		return refuseOpened(tier, slug)
	}
	recordedGate := &record.Gate
	if front == nil {
		log.Say("No " + string(tier) + " deploy of " + slug + " has run yet, so no edge holds a gate: the first one writes the recorded gate")
		return nil
	}
	published := &progressv1.EdgePublication{Kind: string(front.Kind())}
	outcome.Edges = append(outcome.Edges, published)
	publish := gatePublisher(ctx, front, state.Edge, log)
	if publish == nil {
		published.Error = describeFront(front.Kind()) + " runs no gate, so it answers " + describeProtected(tier, slug) + " to anyone: serve them through an edge that gates"
		return errGateUnpublished
	}
	if err := publish(recordedGate, record.Password); err != nil {
		published.Error = err.Error()
		return errors.Join(errGateUnpublished, markGatePending(ctx, p, tier, slug))
	}
	if err := settleGate(ctx, p, tier, slug, recordedGate, publish); err != nil {
		published.Error = err.Error()
		return errors.Join(errGateUnpublished, markGatePending(ctx, p, tier, slug))
	}
	published.Published = true
	ignored, err := reportGateIgnored(ctx, p, front, tier, slug, published)
	if err != nil {
		return err
	}
	if ignored {
		return errGateIgnored
	}
	return nil
}

func gatePublisher(ctx context.Context, front edge.Edge, state edge.StackState, log progress.Log) func(held *gate.Gate, password string) error {
	hook := front.Hooks().PublishGate
	if hook == nil || !front.Facts().Gates {
		return nil
	}
	return func(held *gate.Gate, password string) error {
		log.Say("Publishing the " + string(state.Tier) + " gate of " + state.Slug + " to " + describeFront(front.Kind()))
		return hook(ctx, edge.GatePublication{State: state, Gate: gateForEdge(front, held, password), Say: log.Say})
	}
}

func settleGate(ctx context.Context, p provider.Provider, tier environment.Tier, slug string, written *gate.Gate, publish func(held *gate.Gate, password string) error) error {
	for range maxGateWrites {
		var (
			current  *gate.Gate
			password string
		)
		settled := false
		err := stackrecords.ChangeGates(ctx, p.KeyValues(), p.Cipher(), tier, func(records map[string]stackrecords.GateRecord) error {
			record, recorded := records[slug]
			current, password = record.GateInForce(), record.Password
			settled = sameGate(current, written)
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
			return errGateUnpublished
		}
		if err := publish(current, password); err != nil {
			return err
		}
		written = current
	}
	return errGateUnpublished
}

func markGatePending(ctx context.Context, p provider.Provider, tier environment.Tier, slug string) error {
	return stackrecords.ChangeGates(ctx, p.KeyValues(), p.Cipher(), tier, func(records map[string]stackrecords.GateRecord) error {
		if record, recorded := records[slug]; recorded {
			record.Pending = true
			records[slug] = record
		}
		return nil
	})
}
