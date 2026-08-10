package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"
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
	State        string         `json:"state"`
	Entries      int            `json:"entries,omitempty"`
	Loaded       int            `json:"loaded,omitempty"`
	Failures     []warmFailure  `json:"failures,omitempty"`
	StoppedBy    string         `json:"stoppedBy,omitempty"`
	Skipped      []string       `json:"skipped,omitempty"`
	SkippedCount int            `json:"skippedCount,omitempty"`
	Uncounted    string         `json:"uncounted,omitempty"`
	Bytes        int64          `json:"bytes,omitempty"`
	Key          string         `json:"key,omitempty"`
	Source       bytecodeSource `json:"source"`
	Uploaded     *bool          `json:"uploaded,omitempty"`
	Error        string         `json:"error,omitempty"`
}

const warmInvocationBudget = 30 * time.Second

func warmLoadDeadline(ctx context.Context) (time.Time, bool) {
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(warmInvocationBudget)
	}
	load := deadline.Add(-bytecodeUploadBudget - completionMargin)
	if !load.After(time.Now()) {
		return time.Time{}, false
	}
	return load, true
}

func (m *Membrane) answerWarmInvocation(ctx context.Context, rw *responseWriter) error {
	summary, err := json.Marshal(m.warmBytecodeCache(ctx))
	if err != nil {
		return rw.closeWithError(errTypeUpstream, err.Error())
	}
	fmt.Fprintf(os.Stderr, "ocel: warm invocation: %s\n", summary)
	if _, err := rw.Write(summary); err != nil {
		return err
	}
	return rw.Close()
}

func (m *Membrane) warmBytecodeCache(ctx context.Context) warmSummary {
	source := m.bytecodeCacheSource()
	if m.bytecodeCached() {
		return warmSummary{State: warmStateAlreadyCached, Key: m.bytecodeKey, Source: source}
	}
	if m.bytecode == nil {
		return warmSummary{State: warmStateDisabled, Source: source, Error: "this deployment resolved no bytecode cache identity"}
	}

	deadline, ok := warmLoadDeadline(ctx)
	if !ok {
		return warmSummary{State: warmStateFailed, Source: source, Error: "no time left to warm the compile cache"}
	}

	report, waiter, answered := m.warmCompileCache(ctx, deadline)
	defer m.endWarmExchange()

	summary := warmSummary{Key: m.bytecode.key, Source: source}
	if !m.claimBytecodeUpload() {
		summary.State = warmStateFailed
		summary.Error = "this instance already spent its one compile cache upload"
		return summary
	}

	outcome := m.bytecode.run(ctx)

	if !answered {
		report, answered = collectWarmReport(waiter)
	}
	summary.count(report, answered)

	if outcome.bytes > 0 {
		summary.Bytes = outcome.bytes
	}
	if outcome.existed {
		summary.State = warmStateAlreadyCached
		return summary
	}
	summary.Uploaded = &outcome.uploaded
	if outcome.uploaded {
		summary.State = warmStatePublished
		return summary
	}
	summary.State = warmStateFailed
	summary.Error = outcome.reason
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
