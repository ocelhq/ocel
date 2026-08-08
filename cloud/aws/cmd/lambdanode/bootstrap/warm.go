package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// warmInvocation is the payload a deploy sends to have this instance load
// every entry in the bundle and publish the compile cache that produces. Its
// shape is the whole authorization story: these Function URLs are AWS_IAM and
// the edge composes the event envelope itself, so public traffic can only ever
// influence headers and body — it can never produce this top-level object.
// Authorization is IAM on lambda:InvokeFunction, which is why there is no
// signed header, no deployment key and nothing here to verify.
type warmInvocation struct {
	Ocel *struct {
		Warm int `json:"warm"`
	} `json:"ocel"`
}

// isWarmInvocation is asked before the event parse, because a warm payload is
// not a Function URL event and would otherwise parse as an empty one — a GET
// of "" that the app would answer with a 404 nobody asked for.
func isWarmInvocation(payload []byte) bool {
	var w warmInvocation
	if json.Unmarshal(payload, &w) != nil {
		return false
	}
	return w.Ocel != nil && w.Ocel.Warm > 0
}

// The four states a warm invocation can answer with. published and failed are
// the two outcomes of a real pass; already-cached and disabled are the two
// ways there was nothing to do, and neither is a reason for the deploy to
// retry. failed is reserved for a pass that could publish nothing at all:
// anything the instance did manage to load is worth publishing, so a walk
// nobody could account for still reports the state of the object it produced.
const (
	warmStatePublished     = "published"
	warmStateAlreadyCached = "already-cached"
	warmStateDisabled      = "disabled"
	warmStateFailed        = "failed"
)

// warmSummary is what the deploy reads back. Uploaded is a pointer so the real
// pass can report false — a cache that was loaded and then refused by the
// ceiling or by S3 — while the two states that never reached the upload leg
// omit the field rather than claim a number they never measured.
//
// Uncounted is set, and every count omitted, when the publish happened but
// nobody could say what went into it. Reporting the counts as zeros would be
// the same silence dressed as a measurement.
//
// WholeGraphLoadedAtInit is the third, honest answer for a launcher with no
// entry table at all (a plain node app — see warmSummary.count and
// packages/lambda-entrypoints/src/node/entrypoint.mts's warmNode). It is
// deliberately its own field rather than a magic Entries/Loaded value: a
// caller checks one flag instead of having to learn that zero here means "no
// walk to measure" and not "a walk that measured nothing".
//
// Source is the one field that is always reported, never omitted: "none" is a
// real answer — this instance compiled its own cache — and a missing field
// would read the same as one this membrane is too old to send.
type warmSummary struct {
	State                  string         `json:"state"`
	Entries                int            `json:"entries,omitempty"`
	Loaded                 int            `json:"loaded,omitempty"`
	Failures               []warmFailure  `json:"failures,omitempty"`
	StoppedBy              string         `json:"stoppedBy,omitempty"`
	Skipped                []string       `json:"skipped,omitempty"`
	SkippedCount           int            `json:"skippedCount,omitempty"`
	Uncounted              string         `json:"uncounted,omitempty"`
	WholeGraphLoadedAtInit bool           `json:"wholeGraphLoadedAtInit,omitempty"`
	Bytes                  int64          `json:"bytes,omitempty"`
	Key                    string         `json:"key,omitempty"`
	Source                 bytecodeSource `json:"source"`
	Uploaded               *bool          `json:"uploaded,omitempty"`
	Error                  string         `json:"error,omitempty"`
}

// warmInvocationBudget is what the load window is measured against when the
// invocation carries no deadline at all — only reachable off Lambda, since the
// Runtime API always sets one.
const warmInvocationBudget = 30 * time.Second

// warmLoadDeadline is the instant loading has to stop: the invocation's own
// deadline less what the publish leg is promised (bytecodeUploadBudget) and
// what the runtime needs to call /next (completionMargin). A pass killed
// mid-load by the function timeout publishes nothing at all, so entries are
// only worth loading up to the point where there is provably time left to
// flush, archive and PUT what they produced. It reports false once that point
// has passed, which is the one case where asking node to load anything would
// be pure cost.
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

// answerWarmInvocation writes the summary back as the whole response payload.
// It adds no prelude: no Function URL is involved, and a prelude would only be
// one more layer for the deploy to see through — whether Lambda's streaming
// layer wraps a buffered Invoke's payload in one regardless is a question this
// side cannot answer, so the deploy reads the summary out of either shape
// (parseWarmReply) rather than either side guessing.
//
// It also lands the summary on stderr, which is where an invocation whose
// answer never reached the caller can still be diagnosed.
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

// warmBytecodeCache runs the pass and reports which of the four states it
// reached. Nothing it does may fail the invocation or leave the runtime loop:
// every leg that can go wrong ends as a state in the summary, exactly as the
// post-invocation upload ends as a line on stderr.
//
// The publish leg does not depend on node's report. The compile cache is on
// disk that both ends share and the flush is the membrane's own call, so a
// report that never came, came late, or said the artifact has no warm
// capability at all costs the counts — never the cache. The primary entry was
// loaded at INIT whatever else happened, so there is always something to
// publish, and publishing nothing when something is available is the one
// outcome this whole feature exists to avoid.
func (m *Membrane) warmBytecodeCache(ctx context.Context) warmSummary {
	// A rehydrate hit already proved the object exists, which is why bringUp
	// left no upload leg behind: loading every entry could not publish
	// anything, and answering here rather than doing it is what makes a deploy
	// retry idempotent and near-free.
	source := m.bytecodeCacheSource()
	if m.bytecodeCached() {
		// The key comes from the resolution init already held: a deploy cannot
		// compose it for itself, never having learned node's version, and this
		// is the only answer that carries one for a hit.
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

	// The publish leg gives a report that overran the load deadline a second
	// chance to land: a single slow require is the whole reason one arrives
	// late, and the counts it carries are the difference between naming what
	// stayed cold and reporting nothing at all.
	if !answered {
		report, answered = collectWarmReport(waiter)
	}
	summary.count(report, answered)

	if outcome.bytes > 0 {
		summary.Bytes = outcome.bytes
	}
	// An object another instance got there first with is the cache landing,
	// not this pass failing: the PUT is create-if-absent, so the deployment
	// has what it came for either way.
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

// warmReportStateLoadedAtInit mirrors the node entrypoint's own spelling
// (packages/lambda-entrypoints/src/node/entrypoint.mts's warmNode, via
// WarmReport.state in packages/lambda-entrypoints/src/shared/membrane.mts):
// the honest answer for a launcher with no entry table to walk at all,
// because the whole module graph was already loaded before this handler
// could ever run.
const warmReportStateLoadedAtInit = "loaded-at-init"

// count folds node's report into the summary, or records why there is none to
// fold. An artifact with no warm capability is the same story as a report that
// never arrived: the walk is unaccounted for, and only the counts are lost.
//
// A report naming warmReportStateLoadedAtInit is neither of those — it is a
// real, positive answer, just not a walk — so it gets its own branch rather
// than falling into the default one: reporting Entries/Loaded as 0 there
// would look exactly like a walk that ran and covered nothing, which
// WholeGraphLoadedAtInit exists to be told apart from.
func (s *warmSummary) count(report compileCacheWarmedPayload, answered bool) {
	switch {
	case !answered:
		s.Uncounted = "node did not report back on the compile-cache warm"
	case !report.OK:
		s.Uncounted = "this artifact has no compile-cache warm capability: " + report.State
	case report.State == warmReportStateLoadedAtInit:
		s.WholeGraphLoadedAtInit = true
		s.Bytes = report.Bytes
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
