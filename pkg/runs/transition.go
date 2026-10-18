package runs

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/ocelhq/ocel/pkg/provider"
)

const cutOffAtCap = "its last attempt was cut off before the worker answered, and it has no attempts left"

type readiness int

const (
	runnable readiness = iota
	stale
	held
	expired
	early
	exhausted
)

func readinessOf(run Run, msg Message, now time.Time) (readiness, time.Duration) {
	executing := run.Status == provider.RunExecuting
	switch {
	case !IsUnfinished(run.Status) || run.Delivery != msg.Delivery:
		return stale, 0
	case executing && run.LeaseUntil.After(now):
		return held, run.LeaseUntil.Sub(now)
	case !executing && run.Attempts == 0 && isPast(run.ExpiresAt, now):
		return expired, 0
	case !executing && run.DueAt.After(now.Add(dueSlack)):
		return early, run.DueAt.Sub(now)
	case run.Attempts >= run.MaxAttempts:
		return exhausted, 0
	}
	return runnable, 0
}

func isPast(at, now time.Time) bool { return !at.IsZero() && !at.After(now) }

func exhaustedReason(run Run) string {
	if run.Status == provider.RunExecuting {
		return cutOffAtCap
	}
	return fmt.Sprintf("its %d attempts ran out before one settled the run", run.Attempts)
}

func (r *Run) claim(now time.Time, lease time.Duration) {
	r.Status, r.Error, r.Held = provider.RunExecuting, "", ""
	r.Attempts++
	if r.StartedAt.IsZero() {
		r.StartedAt = now
	}
	r.LeaseUntil = now.Add(lease)
}

func (r *Run) settle(status provider.RunStatus, output json.RawMessage, reason string, payload json.RawMessage, now time.Time) {
	r.Status, r.Output, r.Error, r.FinishedAt = status, output, reason, now
	r.LeaseUntil, r.Held = time.Time{}, ""
	if status != provider.RunCompleted && len(r.Payload) == 0 {
		r.Payload = payload
	}
}

func (r *Run) requeue(reason string) {
	r.Status, r.Error, r.LeaseUntil = provider.RunQueued, reason, time.Time{}
}

func (r *Run) cancel(reason string, now time.Time) {
	r.Status, r.FinishedAt, r.LeaseUntil, r.Held = provider.RunCanceled, now, time.Time{}, ""
	if reason != "" {
		r.Error = reason
	}
}

type verdict struct {
	status     provider.RunStatus
	output     json.RawMessage
	reason     string
	retryAfter time.Duration
}

func (v verdict) retries() bool { return v.status == provider.RunQueued }

func verdictOn(run Run, answer Answer, task bool, retry provider.RetryPolicy) (verdict, bool) {
	switch answer.Outcome {
	case Canceled:
		return verdict{}, false
	case Interrupted:
		if run.Attempts >= run.MaxAttempts {
			return verdict{status: provider.RunFailed, reason: cutOffAtCap}, true
		}
		return verdict{status: provider.RunQueued}, true
	case Succeeded:
		return verdict{status: provider.RunCompleted, output: answer.Output}, true
	case Aborted, Refused:
		return verdict{status: provider.RunFailed, reason: answer.Reason}, true
	case TimedOut:
		if task {
			return verdict{status: provider.RunTimedOut, reason: answer.Reason}, true
		}
	}
	if run.Attempts >= run.MaxAttempts {
		return verdict{status: provider.RunFailed, reason: answer.Reason}, true
	}
	return verdict{status: provider.RunQueued, reason: answer.Reason, retryAfter: Backoff(retry, run.Attempts, Jitter())}, true
}

func (r *Run) apply(v verdict, payload json.RawMessage, now time.Time) {
	if v.retries() {
		r.requeue(v.reason)
		return
	}
	r.settle(v.status, v.output, v.reason, payload, now)
}

func statusAt(run Run, now time.Time, deployed bool) provider.RunStatus {
	switch {
	case IsUnfinished(run.Status) && !deployed:
		return provider.RunCanceled
	case IsWaiting(run.Status) && run.Attempts == 0 && isPast(run.ExpiresAt, now):
		return provider.RunExpired
	case run.Status == provider.RunDelayed && !run.DueAt.After(now):
		return provider.RunQueued
	}
	return run.Status
}
