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
	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
)

const (
	leaseTerm        = 2 * time.Minute
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

func (s *stack) stageLease(c Clients, pointer, holder string) stageLease {
	partition := keyvalue.Partition{
		Tier: s.tier(),
		Root: keyvalue.RootRouters,
		Path: []string{string(Kind), naming.Sanitize(s.slug())},
	}
	return stageLease{
		keyValues: awsports.KeyValues{Dynamo: c.Dynamo, Tables: awsports.Table(s.own.StateTable)},
		key:       partition.Key("stage", pointerOr(pointer)),
		pointer:   pointerOr(pointer),
		holder:    holder,
		now:       time.Now,
		wait:      s.p.leaseWait,
	}
}

func (l stageLease) hold(ctx context.Context, work func() error) error {
	revision, err := l.acquire(ctx)
	if err != nil {
		return err
	}
	worked := work()
	released := l.keyValues.Remove(context.WithoutCancel(ctx), l.key, revision)
	if errors.Is(released, keyvalue.ErrNotFound) || errors.Is(released, keyvalue.ErrStale) {
		released = nil
	}
	return errors.Join(worked, released)
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
	lease := s.stageLease(c, "", "")
	held, err := lease.keyValues.List(ctx, lease.key.Partition, "stage")
	if err != nil {
		return fmt.Errorf("read the leases on %s's stages: %w", s.slug(), err)
	}
	for _, entry := range held {
		if err := keyvalue.Forget(ctx, lease.keyValues, entry.Key); err != nil {
			return fmt.Errorf("let go of %s: %w", entry.Key, err)
		}
	}
	return nil
}
