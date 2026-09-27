package envsource

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"sync"
	"time"

	"github.com/ocelhq/ocel/pkg/envvars"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const (
	defaultSyncInterval = 60 * time.Second
	syncBackoffCeiling  = 15 * time.Minute
	maxBackoffDoublings = 16
	statusWriteTimeout  = 5 * time.Second
)

type Sync struct {
	Store    envvars.Store
	Class    edge.Class
	Login    Login
	Now      func() time.Time
	Interval time.Duration

	mu        sync.Mutex
	opened    map[string]openedSource
	digestKey DigestKey
}

type openedSource struct {
	source     Source
	credential Credential
}

func (s *Sync) now() time.Time {
	if s.Now == nil {
		return time.Now()
	}
	return s.Now()
}

func (s *Sync) interval() time.Duration {
	if s.Interval <= 0 {
		return defaultSyncInterval
	}
	return s.Interval
}

func (s *Sync) CopyScheduledEveryInterval(ctx context.Context, report func(error)) {
	for {
		if err := s.CopyScheduled(ctx); err != nil && report != nil && ctx.Err() == nil {
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

func (s *Sync) budget() time.Duration {
	interval := s.interval()
	return interval - interval/6
}

func (s *Sync) CopyScheduled(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, s.budget())
	defer cancel()
	registrations, err := Registrations(ctx, s.Store.Records, s.Class)
	if err != nil {
		return err
	}
	var keys []string
	groups := map[string][]Registration{}
	var failed []error
	for _, registration := range registrations {
		if !registration.Descriptor.IsScheduled() {
			continue
		}
		key, err := s.keyOf(ctx, registration)
		if err != nil {
			failed = append(failed, err)
			continue
		}
		if _, seen := groups[key]; !seen {
			keys = append(keys, key)
		}
		groups[key] = append(groups[key], registration)
	}
	due := make([]string, 0, len(keys))
	attemptedAt := map[string]time.Time{}
	for _, key := range keys {
		status, _, err := readStatus(ctx, s.Store.Records, s.Class, key)
		if err != nil {
			failed = append(failed, err)
			continue
		}
		if status.RetryAt.After(s.now()) {
			continue
		}
		due = append(due, key)
		attemptedAt[key] = status.LastAttemptAt
	}
	slices.SortStableFunc(due, func(a, b string) int { return attemptedAt[a].Compare(attemptedAt[b]) })
	deadline, _ := ctx.Deadline()
	for i, key := range due {
		left := time.Until(deadline)
		if left <= 0 {
			break
		}
		share, cancelShare := context.WithTimeout(ctx, left/time.Duration(len(due)-i))
		_, _, err := s.copyGroup(share, key, groups[key], nil)
		cancelShare()
		if err != nil {
			failed = append(failed, err)
		}
	}
	return errors.Join(failed...)
}

func (s *Sync) CopyProject(ctx context.Context, registration Registration) (CopyResult, error) {
	return s.CopyProjectFrom(ctx, registration, nil)
}

func (s *Sync) CopyProjectFrom(ctx context.Context, registration Registration, source Source) (CopyResult, error) {
	key, err := s.keyOf(ctx, registration)
	if err != nil {
		return CopyResult{}, err
	}
	results, failure, err := s.copyGroup(ctx, key, []Registration{registration}, source)
	if failure != nil {
		return CopyResult{}, failure
	}
	if err != nil {
		return CopyResult{}, err
	}
	return results[0], nil
}

func (s *Sync) Open(ctx context.Context, registration Registration) (Source, error) {
	key, err := s.keyOf(ctx, registration)
	if err != nil {
		return nil, err
	}
	return s.open(ctx, key, registration)
}

func (s *Sync) keyOf(ctx context.Context, registration Registration) (string, error) {
	key, err := DedupeKey(ctx, s.Store, s.scope(registration), registration.Descriptor)
	if err != nil || key == registration.DedupeKey {
		return key, err
	}
	if err := rekey(ctx, s.Store, s.Class, registration.Project); err != nil {
		return "", err
	}
	return key, nil
}

func (s *Sync) copyGroup(ctx context.Context, key string, group []Registration, source Source) ([]CopyResult, error, error) {
	attemptedAt := s.now()
	results, urls, failure := s.readAndCopy(ctx, key, group, source, attemptedAt)
	if failure != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		failure = fmt.Errorf("ran out of its share of the sync's time, and waits for the next sync so the env sources after it get theirs: %w", failure)
	}
	statusCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), statusWriteTimeout)
	defer cancel()
	err := writeStatus(statusCtx, s.Store.Records, s.Class, key, func(status *Status) {
		status.LastAttemptAt = attemptedAt
		if failure == nil {
			status.EnvSource = results[0].EnvSource
			status.URLs = urls
			status.LastSuccessAt = attemptedAt
			status.LastError = ""
			status.ConsecutiveFailures = 0
			status.RetryAt = time.Time{}
			return
		}
		status.EnvSource = group[0].Descriptor.ID()
		status.LastError = failure.Error()
		status.ConsecutiveFailures++
		status.RetryAt = attemptedAt.Add(max(s.backoff(status.ConsecutiveFailures), retryAfterOf(failure)))
	})
	if failure != nil {
		s.forgetOpened(key)
	}
	return results, failure, err
}

func (s *Sync) readAndCopy(ctx context.Context, key string, group []Registration, source Source, readAt time.Time) ([]CopyResult, map[string]string, error) {
	if source == nil {
		opened, err := s.open(ctx, key, group[0])
		if err != nil {
			return nil, nil, err
		}
		source = opened
	}
	var folders []string
	for _, registration := range group {
		folders = append(folders, registration.Folders...)
	}
	folders = slices.Compact(slices.Sorted(slices.Values(folders)))
	digestKey, err := s.ensureDigestKey(ctx)
	if err != nil {
		return nil, nil, err
	}
	read, err := source.Read(ctx, folders)
	if err != nil {
		return nil, nil, err
	}
	results := make([]CopyResult, 0, len(group))
	for _, registration := range group {
		result, err := CopyValues(ctx, s.Store, s.scope(registration), digestKey, source.ID(), readAt, read, registration.Folders, registration.Credentials())
		if err != nil {
			return nil, nil, err
		}
		results = append(results, result)
	}
	urls := map[string]string{}
	for _, folder := range folders {
		if url := source.URL(envvars.Cell{Folder: folder}); url != "" {
			urls[folder] = url
		}
	}
	return results, urls, nil
}

func (s *Sync) open(ctx context.Context, key string, registration Registration) (Source, error) {
	credential, err := ReadCredential(ctx, s.Store, s.scope(registration), registration.Descriptor, s.Login)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if current, found := s.opened[key]; found && current.credential.sameAs(credential) {
		return current.source, nil
	}
	source := NewInfisical(*registration.Descriptor.Infisical, credential, s.Login.Client)
	if s.opened == nil {
		s.opened = map[string]openedSource{}
	}
	s.opened[key] = openedSource{source: source, credential: credential}
	return source, nil
}

func (s *Sync) ensureDigestKey(ctx context.Context) (DigestKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.digestKey.secret) > 0 {
		return s.digestKey, nil
	}
	key, err := EnsureDigestKey(ctx, s.Store, s.Class)
	if err != nil {
		return DigestKey{}, err
	}
	s.digestKey = key
	return key, nil
}

func (s *Sync) forgetOpened(key string) {
	s.mu.Lock()
	delete(s.opened, key)
	s.mu.Unlock()
}

func (s *Sync) backoff(failures int) time.Duration {
	ceiling := min(s.interval()<<min(failures-1, maxBackoffDoublings), syncBackoffCeiling)
	return ceiling/2 + time.Duration(rand.Int64N(int64(ceiling/2)+1))
}

func (s *Sync) scope(registration Registration) envvars.Scope {
	return envvars.Scope{Project: registration.Project, Class: s.Class}
}
