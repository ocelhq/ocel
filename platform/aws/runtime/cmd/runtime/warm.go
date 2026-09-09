package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/ocelhq/ocel/platform/aws/runtime/bytecode"
)

type warmInvocation struct {
	Ocel *struct {
		Warm int `json:"warm"`
	} `json:"ocel"`
}

func isWarmInvocation(payload []byte) bool {
	var w warmInvocation
	if json.Unmarshal(payload, &w) != nil {
		return false
	}
	return w.Ocel != nil && w.Ocel.Warm > 0
}

const (
	warmStatePublished     = "published"
	warmStateAlreadyCached = "already-cached"
	warmStateDisabled      = "disabled"
	warmStateFailed        = "failed"
)

type warmSummary struct {
	State        string          `json:"state"`
	Entries      int             `json:"entries,omitempty"`
	Loaded       int             `json:"loaded,omitempty"`
	Failures     []warmFailure   `json:"failures,omitempty"`
	StoppedBy    string          `json:"stoppedBy,omitempty"`
	Skipped      []string        `json:"skipped,omitempty"`
	SkippedCount int             `json:"skippedCount,omitempty"`
	Uncounted    string          `json:"uncounted,omitempty"`
	Bytes        int64           `json:"bytes,omitempty"`
	Key          string          `json:"key,omitempty"`
	Source       bytecode.Source `json:"source"`
	Uploaded     *bool           `json:"uploaded,omitempty"`
	Error        string          `json:"error,omitempty"`
}

const warmInvocationBudget = 30 * time.Second

func warmLoadDeadline(ctx context.Context) (time.Time, bool) {
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(warmInvocationBudget)
	}
	load := deadline.Add(-bytecode.UploadBudget - completionMargin)
	if !load.After(time.Now()) {
		return time.Time{}, false
	}
	return load, true
}

func answerWarm(ctx context.Context, c controlledChild, rw *responseWriter) error {
	if c == nil {
		return writeWarmSummary(rw, warmSummary{
			State:  warmStateDisabled,
			Source: bytecode.SourceNone,
			Error:  "this app carries no compile cache to warm",
		})
	}
	return c.answerWarmInvocation(ctx, rw)
}

func (m *nodeChild) answerWarmInvocation(ctx context.Context, rw *responseWriter) error {
	return writeWarmSummary(rw, m.warmBytecodeCache(ctx))
}

func writeWarmSummary(rw *responseWriter, s warmSummary) error {
	summary, err := json.Marshal(s)
	if err != nil {
		return rw.closeWithError(errTypeUpstream, err.Error())
	}
	fmt.Fprintf(os.Stderr, "ocel: warm invocation: %s\n", summary)
	if _, err := rw.Write(summary); err != nil {
		return err
	}
	return rw.Close()
}

func (m *nodeChild) warmBytecodeCache(ctx context.Context) warmSummary {
	source := m.cacheSource()
	if m.cached() {
		return warmSummary{State: warmStateAlreadyCached, Key: m.cache.Key(), Source: source}
	}
	if m.cache == nil {
		return warmSummary{State: warmStateDisabled, Source: source, Error: "this deployment resolved no bytecode cache identity"}
	}

	deadline, ok := warmLoadDeadline(ctx)
	if !ok {
		return warmSummary{State: warmStateFailed, Source: source, Error: "no time left to warm the compile cache"}
	}

	if err := m.awaitReadyBy(ctx, deadline); err != nil {
		return warmSummary{State: warmStateFailed, Source: source, Key: m.cache.Key(), Error: err.Error()}
	}

	report, waiter, answered := m.warmCompileCache(ctx, deadline)
	defer m.endWarmExchange()

	summary := warmSummary{Key: m.cache.Key(), Source: source}
	if !m.claimBytecodeUpload() {
		summary.State = warmStateFailed
		summary.Error = "this instance already spent its one compile cache upload"
		return summary
	}

	outcome := m.cache.Upload(ctx, uploadBy(ctx), m.flushed)

	if !answered {
		report, answered = collectWarmReport(waiter)
	}
	summary.count(report, answered)

	if outcome.Bytes > 0 {
		summary.Bytes = outcome.Bytes
	}
	if outcome.Existed {
		summary.State = warmStateAlreadyCached
		return summary
	}
	summary.Uploaded = &outcome.Uploaded
	if outcome.Uploaded {
		summary.State = warmStatePublished
		return summary
	}
	summary.State = warmStateFailed
	summary.Error = outcome.Reason
	return summary
}

func (s *warmSummary) count(report compileCacheWarmedPayload, answered bool) {
	switch {
	case !answered:
		s.Uncounted = "node did not report back on the compile-cache warm"
	case !report.OK:
		s.Uncounted = "this artifact has no compile-cache warm capability: " + report.State
	default:
		s.Entries = report.Entries
		s.Loaded = report.Loaded
		s.Failures = report.Failures
		s.StoppedBy = report.StoppedBy
		s.Skipped = report.Skipped
		s.SkippedCount = report.SkippedCount
		s.Bytes = report.Bytes
	}
}
