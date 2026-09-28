package apigateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
)

const (
	leaseTerm        = 2 * time.Minute
	leaseWriteWindow = leaseTerm / 2
	leaseAttempts    = 12
	leaseWaitBase    = 250 * time.Millisecond
	leaseWaitCeiling = 5 * time.Second
)

type stageLease struct {
	keyValues keyvalue.Store
	key       keyvalue.Key
	pointer   string
	holder    string
	now       func() time.Time
	wait      func(context.Context, time.Duration) error
}

type leaseRecord struct {
	Holder string `json:"holder"`
	Until  int64  `json:"until"`
}

func (s *stack) stagePartition() keyvalue.Partition {
	return keyvalue.Partition{
		Tier: s.tier(),
		Root: keyvalue.RootRouters,
		Path: []string{string(Kind), naming.Sanitize(s.slug())},
	}
}

func (s *stack) stateKeyValues(c Clients) keyvalue.Store {
	return awsports.KeyValues{Dynamo: c.Dynamo, Tables: awsports.Table(s.own.StateTable)}
}

func (s *stack) newStageLease(c Clients, pointer, holder string) stageLease {
	return stageLease{
		keyValues: s.stateKeyValues(c),
		key:       s.stagePartition().Key("stage", router.ResolvePointer(pointer)),
		pointer:   router.ResolvePointer(pointer),
		holder:    holder,
		now:       time.Now,
		wait:      s.p.leaseWait,
	}
}

type heldLease struct {
	lease    stageLease
	revision keyvalue.Revision
}

func (l stageLease) hold(ctx context.Context, work func(held *heldLease) error) error {
	revision, err := l.acquire(ctx)
	if err != nil {
		return err
	}
	held := &heldLease{lease: l, revision: revision}
	worked := work(held)
	released := l.keyValues.Remove(context.WithoutCancel(ctx), l.key, held.revision)
	if errors.Is(released, keyvalue.ErrNotFound) || errors.Is(released, keyvalue.ErrStale) {
		released = nil
	}
	return errors.Join(worked, released)
}

func (h *heldLease) renew(ctx context.Context) (context.Context, context.CancelFunc, error) {
	l := h.lease
	renewed := l.now()
	encoded, err := json.Marshal(leaseRecord{Holder: l.holder, Until: renewed.Add(leaseTerm).Unix()})
	if err != nil {
		return nil, nil, err
	}
	revision, err := l.keyValues.Write(ctx, keyvalue.Entry{Key: l.key, Value: encoded, Revision: h.revision})
	if errors.Is(err, keyvalue.ErrStale) || errors.Is(err, keyvalue.ErrNotFound) {
		return nil, nil, refusal.Refuse(refusal.CodeBusy,
			"this promote held the %s stage of %s past its %s term, and another promote has since taken it, so this one stopped rather than move the stage under it. Promote again once the other one has finished",
			stageName, l.pointer, leaseTerm)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("renew this promote's hold on the %s stage: %w", stageName, err)
	}
	h.revision = revision
	bounded, stop := context.WithDeadline(ctx, renewed.Add(leaseWriteWindow))
	return bounded, stop, nil
}

func (l stageLease) acquire(ctx context.Context) (keyvalue.Revision, error) {
	for attempt := range leaseAttempts {
		if attempt > 0 {
			if err := l.wait(ctx, leaseDelay(attempt-1)); err != nil {
				return "", err
			}
		}
		recorded, err := keyvalue.ReadOrEmpty(ctx, l.keyValues, l.key)
		if err != nil {
			return "", fmt.Errorf("read who is moving the %s stage: %w", stageName, err)
		}
		if len(recorded.Value) > 0 {
			var held leaseRecord
			if err := json.Unmarshal(recorded.Value, &held); err != nil {
				return "", fmt.Errorf("read who is moving the %s stage: %w", stageName, err)
			}
			if held.Holder != l.holder && l.now().Unix() < held.Until {
				continue
			}
		}
		if recorded.Value, err = json.Marshal(leaseRecord{Holder: l.holder, Until: l.now().Add(leaseTerm).Unix()}); err != nil {
			return "", err
		}
		revision, err := l.keyValues.Write(ctx, recorded)
		if errors.Is(err, keyvalue.ErrStale) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("record that this promote is moving the %s stage: %w", stageName, err)
		}
		return revision, nil
	}
	return "", refusal.Refuse(refusal.CodeBusy,
		"another promote held the %s stage of %s on every one of %d attempts, and one promote moves a stage at a time so that the stage ends on the release the ledger names. Promote again once the other one has finished",
		stageName, l.pointer, leaseAttempts)
}

func leaseDelay(attempt int) time.Duration {
	delay := min(leaseWaitBase<<attempt, leaseWaitCeiling)
	return delay - time.Duration(rand.Float64()*float64(delay/4))
}

func (s *stack) forgetStageLeases(ctx context.Context, c Clients) error {
	if s.own.StateTable == "" {
		return nil
	}
	keyValues := s.stateKeyValues(c)
	held, err := keyValues.List(ctx, s.stagePartition(), "stage")
	if err != nil {
		return fmt.Errorf("read the leases on %s's stages: %w", s.slug(), err)
	}
	for _, entry := range held {
		if err := keyvalue.Forget(ctx, keyValues, entry.Key); err != nil {
			return fmt.Errorf("let go of %s: %w", entry.Key, err)
		}
	}
	return nil
}
