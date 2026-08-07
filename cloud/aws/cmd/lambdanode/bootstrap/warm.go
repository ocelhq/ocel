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
// retry.
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
type warmSummary struct {
	State     string        `json:"state"`
	Entries   int           `json:"entries,omitempty"`
	Loaded    int           `json:"loaded,omitempty"`
	Failures  []warmFailure `json:"failures,omitempty"`
	StoppedBy string        `json:"stoppedBy,omitempty"`
	Bytes     int64         `json:"bytes,omitempty"`
	Key       string        `json:"key,omitempty"`
	Uploaded  *bool         `json:"uploaded,omitempty"`
	Error     string        `json:"error,omitempty"`
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

// answerWarmInvocation writes the summary back as the whole response payload —
// no prelude framing, since no Function URL is involved and the deploy reads
// these bytes as JSON. It also lands the summary on stderr, which is where an
// invocation whose answer never reached the caller can still be diagnosed.
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
func (m *Membrane) warmBytecodeCache(ctx context.Context) warmSummary {
	// A rehydrate hit already proved the object exists, which is why bringUp
	// left no upload leg behind: loading every entry could not publish
	// anything, and answering here rather than doing it is what makes a deploy
	// retry idempotent and near-free.
	if m.bytecodeCached {
		return warmSummary{State: warmStateAlreadyCached}
	}
	if m.bytecode == nil {
		return warmSummary{State: warmStateDisabled}
	}

	deadline, ok := warmLoadDeadline(ctx)
	if !ok {
		return warmSummary{State: warmStateFailed, Error: "no time left to warm the compile cache"}
	}
	reply, answered := m.warmCompileCache(ctx, deadline)
	if !answered {
		return warmSummary{State: warmStateFailed, Error: "node did not report back on the compile-cache warm"}
	}
	if !reply.OK {
		return warmSummary{State: warmStateDisabled, Error: "this artifact has no compile-cache warm capability: " + reply.State}
	}

	summary := warmSummary{
		Entries:   reply.Entries,
		Loaded:    reply.Loaded,
		Failures:  reply.Failures,
		StoppedBy: reply.StoppedBy,
		Bytes:     reply.Bytes,
		Key:       m.bytecode.key,
	}
	if !m.claimBytecodeUpload() {
		summary.State = warmStateFailed
		summary.Error = "this instance already spent its one compile cache upload"
		return summary
	}

	outcome := m.bytecode.run(ctx)
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
