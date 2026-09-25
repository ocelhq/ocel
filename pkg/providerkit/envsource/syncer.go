package envsource

import (
	"context"
	"errors"
	"math/rand/v2"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/ocelhq/ocel/pkg/providerkit/ports"
	"github.com/ocelhq/ocel/pkg/providerkit/values"
)

const (
	PollInterval = 60 * time.Second
	backoffCap   = 15 * time.Minute
)

type Opener func(ctx context.Context, descriptor Descriptor, credential Credential) (Source, error)

type Syncer struct {
	Store    values.Store
	Class    ports.Class
	Target   Target
	Open     Opener
	Now      func() time.Time
	Interval time.Duration

	mu     sync.Mutex
	opened map[string]Source
}

func (s *Syncer) now() time.Time {
	if s.Now == nil {
		return time.Now()
	}
	return s.Now()
}

func (s *Syncer) interval() time.Duration {
	if s.Interval <= 0 {
		return PollInterval
	}
	return s.Interval
}

func (s *Syncer) Run(ctx context.Context, report func(error)) {
	for {
		if err := s.Poll(ctx); err != nil && report != nil {
			report(err)
		}
		wait := s.interval()
		wait += time.Duration(rand.Int64N(int64(wait)/5+1)) - wait/10
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (s *Syncer) Poll(ctx context.Context) error {
	registrations, err := Registrations(ctx, s.Store.Records, s.Class)
	if err != nil {
		return err
	}
	var order []string
	groups := map[string][]Registration{}
	for _, registration := range registrations {
		if !registration.Descriptor.Standing() {
			continue
		}
		identity := Identity(ctx, s.Store, s.scope(registration), registration.Descriptor)
		if _, seen := groups[identity]; !seen {
			order = append(order, identity)
		}
		groups[identity] = append(groups[identity], registration)
	}
	var failed []error
	for _, identity := range order {
		status, _, err := readStatus(ctx, s.Store.Records, s.Class, identity)
		if err != nil {
			failed = append(failed, err)
			continue
		}
		if status.RetryAt > s.now().Unix() {
			continue
		}
		if _, _, err := s.sync(ctx, identity, groups[identity]); err != nil {
			failed = append(failed, err)
		}
	}
	return errors.Join(failed...)
}

func (s *Syncer) Sync(ctx context.Context, registration Registration) (Report, error) {
	identity := Identity(ctx, s.Store, s.scope(registration), registration.Descriptor)
	reports, failure, err := s.sync(ctx, identity, []Registration{registration})
	if failure != nil {
		return Report{}, failure
	}
	if err != nil {
		return Report{}, err
	}
	return reports[0], nil
}

func (s *Syncer) Source(ctx context.Context, registration Registration) (Source, error) {
	return s.source(ctx, Identity(ctx, s.Store, s.scope(registration), registration.Descriptor), registration)
}

func (s *Syncer) sync(ctx context.Context, identity string, group []Registration) ([]Report, error, error) {
	attempted := s.now()
	reports, failure := s.mirror(ctx, identity, group)
	err := recordStatus(ctx, s.Store.Records, s.Class, identity, func(status *Status) {
		status.LastAttemptAt = attempted.Unix()
		if failure == nil {
			status.Source = reports[0].Source
			status.LastSuccessAt = attempted.Unix()
			status.LastError = ""
			status.Failures = 0
			status.RetryAt = 0
			return
		}
		status.LastError = failure.Error()
		status.Failures++
		status.RetryAt = attempted.Add(s.backoff(status.Failures)).Unix()
	})
	if failure != nil {
		s.forget(identity)
	}
	return reports, failure, err
}

func (s *Syncer) mirror(ctx context.Context, identity string, group []Registration) ([]Report, error) {
	source, err := s.source(ctx, identity, group[0])
	if err != nil {
		return nil, err
	}
	var folders []string
	for _, registration := range group {
		folders = append(folders, registration.Folders...)
	}
	slices.Sort(folders)
	resolved, err := source.Resolve(ctx, slices.Compact(folders))
	if err != nil {
		return nil, err
	}
	reports := make([]Report, 0, len(group))
	for _, registration := range group {
		report, err := Apply(ctx, s.Store, s.scope(registration), source.ID(), resolved, registration.Folders, registration.Credentials())
		if err != nil {
			return nil, err
		}
		reports = append(reports, report)
	}
	return reports, nil
}

func (s *Syncer) source(ctx context.Context, identity string, registration Registration) (Source, error) {
	s.mu.Lock()
	held, open := s.opened[identity]
	s.mu.Unlock()
	if open {
		return held, nil
	}
	credential, err := CredentialFor(ctx, s.Store, s.scope(registration), registration.Descriptor, s.Target)
	if err != nil {
		return nil, err
	}
	opener := s.Open
	if opener == nil {
		opener = s.openInfisical
	}
	source, err := opener(ctx, registration.Descriptor, credential)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	if s.opened == nil {
		s.opened = map[string]Source{}
	}
	s.opened[identity] = source
	s.mu.Unlock()
	return source, nil
}

func (s *Syncer) openInfisical(_ context.Context, descriptor Descriptor, credential Credential) (Source, error) {
	client := s.Target.Client
	if client == nil {
		client = http.DefaultClient
	}
	return NewInfisical(*descriptor.Infisical, credential, client), nil
}

func (s *Syncer) forget(identity string) {
	s.mu.Lock()
	delete(s.opened, identity)
	s.mu.Unlock()
}

func (s *Syncer) backoff(failures int) time.Duration {
	ceiling := min(s.interval()<<min(failures-1, 16), backoffCap)
	return ceiling/2 + time.Duration(rand.Int64N(int64(ceiling/2)+1))
}

func (s *Syncer) scope(registration Registration) values.Scope {
	return values.Scope{Project: registration.Project, Class: s.Class}
}
