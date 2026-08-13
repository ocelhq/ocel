package deploy

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

func TestUploadBatchStatsCountsAndSumsBytes(t *testing.T) {
	t.Parallel()

	s := newUploadBatchStats()
	base := time.Unix(1000, 0)
	s.record(uploadOutcome{Bytes: 100, Start: base, End: base.Add(10 * time.Millisecond), Transferred: true})
	s.record(uploadOutcome{Bytes: 250, Start: base.Add(time.Millisecond), End: base.Add(20 * time.Millisecond), Transferred: true})
	s.record(uploadOutcome{Bytes: 0, Start: base.Add(2 * time.Millisecond), End: base.Add(5 * time.Millisecond), Transferred: true})

	snap := s.snapshot()
	if snap.attempts != 3 {
		t.Fatalf("attempts = %d, want 3", snap.attempts)
	}
	if snap.transferred != 3 {
		t.Fatalf("transferred = %d, want 3", snap.transferred)
	}
	if snap.bytes != 350 {
		t.Fatalf("bytes = %d, want 350", snap.bytes)
	}
	if !snap.start.Equal(base) {
		t.Errorf("start = %v, want %v", snap.start, base)
	}
	if !snap.end.Equal(base.Add(20 * time.Millisecond)) {
		t.Errorf("end = %v, want %v", snap.end, base.Add(20*time.Millisecond))
	}
}

func TestUploadBatchStatsOnlyCountsTransferredObjects(t *testing.T) {
	t.Parallel()

	s := newUploadBatchStats()
	base := time.Unix(1500, 0)
	for i := 0; i < 5000; i++ {
		s.record(uploadOutcome{Start: base, End: base, Transferred: false})
	}

	snap := s.snapshot()
	if snap.attempts != 5000 {
		t.Fatalf("attempts = %d, want 5000: a fully-cached phase still processed every object", snap.attempts)
	}
	if snap.transferred != 0 {
		t.Fatalf("transferred = %d, want 0: nothing crossed the wire", snap.transferred)
	}
	if snap.bytes != 0 {
		t.Fatalf("bytes = %d, want 0", snap.bytes)
	}
}

func TestUploadBatchStatsCapsFailuresKeepingTheFirstN(t *testing.T) {
	t.Parallel()

	s := newUploadBatchStats()
	base := time.Unix(2000, 0)
	for i := 0; i < maxUploadFailureStandouts+5; i++ {
		s.record(uploadOutcome{Start: base, End: base.Add(time.Millisecond), Failed: true, Err: fmt.Errorf("boom %d", i)})
	}

	snap := s.snapshot()
	if len(snap.failures) != maxUploadFailureStandouts {
		t.Fatalf("len(failures) = %d, want %d", len(snap.failures), maxUploadFailureStandouts)
	}
	if snap.attempts != maxUploadFailureStandouts+5 {
		t.Fatalf("attempts = %d, want %d: every attempt still counts even once failures are capped", snap.attempts, maxUploadFailureStandouts+5)
	}
}

func TestUploadBatchStatsDropsCanceledFailures(t *testing.T) {
	t.Parallel()

	s := newUploadBatchStats()
	base := time.Unix(2500, 0)
	s.record(uploadOutcome{Start: base, End: base.Add(time.Millisecond), Failed: true, Err: errors.New("access denied")})
	for i := 0; i < 500; i++ {
		s.record(uploadOutcome{Start: base, End: base.Add(time.Millisecond), Failed: true, Err: context.Canceled})
	}

	snap := s.snapshot()
	if len(snap.failures) != 1 {
		t.Fatalf("len(failures) = %d, want 1: cancellation cascade carries nothing the batch error doesn't already say", len(snap.failures))
	}
	if snap.failures[0].Kind != ErrorKindFailed {
		t.Errorf("kind = %q, want %q", snap.failures[0].Kind, ErrorKindFailed)
	}
}

func TestUploadBatchStatsCapsLatencyStandoutsKeepingTheSlowest(t *testing.T) {
	t.Parallel()

	s := newUploadBatchStats()
	base := time.Unix(3000, 0)
	for i := 0; i < maxUploadLatencyStandouts+5; i++ {
		dur := uploadLatencyOutlierThreshold + time.Duration(i+1)*time.Millisecond
		s.record(uploadOutcome{Bytes: int64(i), Start: base, End: base.Add(dur)})
	}

	snap := s.snapshot()
	if len(snap.slowest) != maxUploadLatencyStandouts {
		t.Fatalf("len(slowest) = %d, want %d", len(snap.slowest), maxUploadLatencyStandouts)
	}
	for _, o := range snap.slowest {
		if o.End.Sub(o.Start) < uploadLatencyOutlierThreshold+6*time.Millisecond {
			t.Fatalf("kept a standout shorter than the evicted ones: %v", o.End.Sub(o.Start))
		}
	}
}

func TestUploadBatchStatsIgnoresFastUploadsUnderThreshold(t *testing.T) {
	t.Parallel()

	s := newUploadBatchStats()
	base := time.Unix(4000, 0)
	s.record(uploadOutcome{Start: base, End: base.Add(time.Millisecond)})

	if snap := s.snapshot(); len(snap.slowest) != 0 {
		t.Fatalf("len(slowest) = %d, want 0", len(snap.slowest))
	}
}

func TestUploadBatchStatsRecordUnderConcurrency(t *testing.T) {
	const n = uploadConcurrency

	s := newUploadBatchStats()
	base := time.Unix(5000, 0)

	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			failed := i%10 == 0
			dur := time.Duration(i%3) * uploadLatencyOutlierThreshold
			var err error
			if failed {
				err = fmt.Errorf("upload %d failed", i)
			}
			s.record(uploadOutcome{
				Bytes:       int64(i),
				Start:       base,
				End:         base.Add(dur),
				Failed:      failed,
				Err:         err,
				Transferred: !failed,
			})
		}(i)
	}
	wg.Wait()

	snap := s.snapshot()
	if snap.attempts != n {
		t.Fatalf("attempts = %d, want %d", snap.attempts, n)
	}
	wantFailures := 0
	for i := 0; i < n; i++ {
		if i%10 == 0 {
			wantFailures++
		}
	}
	if len(snap.failures) != wantFailures {
		t.Fatalf("len(failures) = %d, want %d", len(snap.failures), wantFailures)
	}
}

func TestEmitUploadBatchEmitsOneSpanWithCountsAndBytes(t *testing.T) {
	t.Parallel()

	ft := &fakeTracer{}
	s := newUploadBatchStats()
	base := time.Unix(6000, 0)
	s.record(uploadOutcome{Bytes: 100, Start: base, End: base.Add(time.Millisecond), Transferred: true})
	s.record(uploadOutcome{Bytes: 200, Start: base.Add(time.Millisecond), End: base.Add(2 * time.Millisecond), Transferred: true})

	parent := NewRootStage("Uploading")
	emitUploadBatch(ft, parent.ID, uploadKindStaticAsset, s, nil, base)

	if len(ft.spans) != 1 {
		t.Fatalf("got %d spans, want 1 (no failures or outliers)", len(ft.spans))
	}
	got := ft.spans[0]
	if got.name != "upload static assets" {
		t.Errorf("name = %q, want the fixed batch span name", got.name)
	}
	if got.parentID != parent.ID {
		t.Errorf("parentID = %v, want %v", got.parentID, parent.ID)
	}
	if got.err != nil {
		t.Errorf("err = %v, want nil", got.err)
	}
	if got := attrValue(got.attrs, AttrResourceCount(0).Key); got != "2" {
		t.Errorf("resource count attr = %q, want %q", got, "2")
	}
	if got := attrValue(got.attrs, AttrBytes(0).Key); got != "300" {
		t.Errorf("bytes attr = %q, want %q", got, "300")
	}
}

func TestEmitUploadBatchReportsZeroResourceCountForAFullyCachedPhase(t *testing.T) {
	t.Parallel()

	ft := &fakeTracer{}
	s := newUploadBatchStats()
	base := time.Unix(6500, 0)
	for i := 0; i < 5000; i++ {
		s.record(uploadOutcome{Start: base, End: base.Add(time.Millisecond), Transferred: false})
	}

	emitUploadBatch(ft, NewRootStage("Uploading").ID, uploadKindStaticAsset, s, nil, base)

	if len(ft.spans) != 1 {
		t.Fatalf("got %d spans, want 1: a fully-cached phase still emits its batch span", len(ft.spans))
	}
	got := ft.spans[0]
	if v := attrValue(got.attrs, AttrResourceCount(0).Key); v != "0" {
		t.Errorf("resource count attr = %q, want %q", v, "0")
	}
	if v := attrValue(got.attrs, AttrBytes(0).Key); v != "0" {
		t.Errorf("bytes attr = %q, want %q", v, "0")
	}
}

func TestEmitUploadBatchSkipsWhenNothingHappened(t *testing.T) {
	t.Parallel()

	ft := &fakeTracer{}
	emitUploadBatch(ft, NewRootStage("Uploading").ID, uploadKindPrerenderAsset, newUploadBatchStats(), nil, time.Now())
	if len(ft.spans) != 0 {
		t.Fatalf("got %d spans, want 0 for an empty batch", len(ft.spans))
	}
}

func TestEmitUploadBatchUsesThePhaseStartWhenNoUploadEverRan(t *testing.T) {
	t.Parallel()

	ft := &fakeTracer{}
	phaseStart := time.Unix(6800, 0)
	phaseErr := errors.New("unknown app")

	emitUploadBatch(ft, NewRootStage("Uploading").ID, uploadKindFunctionArtifact, newUploadBatchStats(), phaseErr, phaseStart)

	if len(ft.spans) != 1 {
		t.Fatalf("got %d spans, want 1", len(ft.spans))
	}
	got := ft.spans[0]
	if !got.start.Equal(phaseStart) {
		t.Errorf("start = %v, want the phase start %v", got.start, phaseStart)
	}
	if !got.end.Equal(phaseStart) {
		t.Errorf("end = %v, want the phase start %v", got.end, phaseStart)
	}
}

func TestEmitUploadBatchCapsTheCancellationCascadeButKeepsTheCausalFailure(t *testing.T) {
	t.Parallel()

	ft := &fakeTracer{}
	s := newUploadBatchStats()
	base := time.Unix(7000, 0)

	s.record(uploadOutcome{Start: base, End: base.Add(time.Millisecond), Failed: true, Err: errors.New("access denied for bucket x")})
	const cascade = 5000
	for i := 0; i < cascade; i++ {
		s.record(uploadOutcome{Start: base, End: base.Add(time.Millisecond), Failed: true, Err: context.Canceled})
	}

	parent := NewRootStage("Uploading")
	emitUploadBatch(ft, parent.ID, uploadKindFunctionArtifact, s, nil, base)

	failureSpans := 0
	for _, sp := range ft.spans {
		if sp.name == uploadStandoutName(uploadKindFunctionArtifact, true) {
			failureSpans++
			if sp.err == nil {
				t.Error("failure standout span has no error")
			}
		}
	}
	if failureSpans != 1 {
		t.Fatalf("failure spans = %d, want 1: the cancellation cascade must not each get a span, and the causal failure must survive", failureSpans)
	}
	if len(ft.spans) > maxUploadFailureStandouts+maxUploadLatencyStandouts+1 {
		t.Fatalf("total spans = %d, unbounded relative to the cascade size %d", len(ft.spans), cascade)
	}
}

func TestEmitUploadBatchCapsSlowStandoutsAndKeepsBatchSpanEvenWithoutFailures(t *testing.T) {
	t.Parallel()

	ft := &fakeTracer{}
	s := newUploadBatchStats()
	base := time.Unix(8000, 0)
	const slow = maxUploadLatencyStandouts + 4
	for i := 0; i < slow; i++ {
		dur := uploadLatencyOutlierThreshold + time.Duration(i+1)*time.Millisecond
		s.record(uploadOutcome{Start: base, End: base.Add(dur)})
	}

	emitUploadBatch(ft, NewRootStage("Uploading").ID, uploadKindPrerenderAsset, s, nil, base)

	batchSpans, slowSpans := 0, 0
	for _, sp := range ft.spans {
		switch sp.name {
		case uploadBatchSpanName(uploadKindPrerenderAsset):
			batchSpans++
			if sp.err != nil {
				t.Errorf("batch span err = %v, want nil (no failures occurred)", sp.err)
			}
		case uploadStandoutName(uploadKindPrerenderAsset, false):
			slowSpans++
		}
	}
	if batchSpans != 1 {
		t.Fatalf("batch spans = %d, want 1", batchSpans)
	}
	if slowSpans != maxUploadLatencyStandouts {
		t.Fatalf("slow standout spans = %d, want %d", slowSpans, maxUploadLatencyStandouts)
	}
}

func TestEmitUploadBatchMarksTheBatchSpanFailedWhenAnyUploadFails(t *testing.T) {
	t.Parallel()

	ft := &fakeTracer{}
	s := newUploadBatchStats()
	base := time.Unix(9000, 0)
	s.record(uploadOutcome{Start: base, End: base.Add(time.Millisecond)})
	s.record(uploadOutcome{Start: base, End: base.Add(time.Millisecond), Failed: true, Err: errors.New("boom")})

	emitUploadBatch(ft, NewRootStage("Uploading").ID, uploadKindStaticAsset, s, nil, base)

	for _, sp := range ft.spans {
		if sp.name == uploadBatchSpanName(uploadKindStaticAsset) && sp.err == nil {
			t.Fatal("batch span err = nil, want non-nil: the batch had a failure")
		}
	}
}

func TestUploadStandoutNameAndUploadBatchSpanNameAreAlwaysALiteral(t *testing.T) {
	t.Parallel()

	kinds := []uploadKind{uploadKindFunctionArtifact, uploadKindStaticAsset, uploadKindPrerenderAsset, uploadKind(99)}

	seen := map[string]bool{}
	for _, k := range kinds {
		for _, failed := range []bool{true, false} {
			name := uploadStandoutName(k, failed)
			if name == "" {
				t.Errorf("uploadStandoutName(%d, %v) = empty", k, failed)
			}
			seen[name] = true
		}
		if name := uploadBatchSpanName(k); name == "" {
			t.Errorf("uploadBatchSpanName(%d) = empty", k)
		} else {
			seen[name] = true
		}
	}
	if len(seen) != 12 {
		t.Errorf("got %d distinct strings, want exactly 12 (3 kinds x pass/fail standouts + 3 batch names, plus the unknown-kind fallback of each)", len(seen))
	}
}

func TestUploadFailuresNeverCarryTheRawErrorPastRecord(t *testing.T) {
	t.Parallel()

	const (
		secretBucket = "my-super-secret-bucket-name"
		secretKey    = "assets/proj/web/RELEASE123/logo-with-pii.png"
		secretPath   = "/home/build/.ocel/tmp/RELEASE123/logo-with-pii.png"
		secretURL    = "https://my-super-secret-bucket-name.s3.amazonaws.com/assets/proj/web/RELEASE123/logo-with-pii.png?X-Amz-Signature=abc123"
	)
	rawErr := fmt.Errorf("upload artifact %s/%s: dial %s: connection refused (path %s)", secretBucket, secretKey, secretURL, secretPath)

	s := newUploadBatchStats()
	base := time.Unix(10000, 0)
	s.record(uploadOutcome{Start: base, End: base.Add(time.Millisecond), Failed: true, Err: rawErr})

	snap := s.snapshot()
	if len(snap.failures) != 1 {
		t.Fatalf("failures = %d, want 1", len(snap.failures))
	}
	if snap.failures[0].Kind != ErrorKindFailed {
		t.Errorf("kind = %q, want %q", snap.failures[0].Kind, ErrorKindFailed)
	}

	ft := &fakeTracer{}
	emitUploadBatch(ft, NewRootStage("Uploading").ID, uploadKindFunctionArtifact, s, nil, base)

	forbidden := []string{secretBucket, secretKey, secretPath, secretURL}
	for _, sp := range ft.spans {
		if sp.name == uploadStandoutName(uploadKindFunctionArtifact, true) {
			if sp.err != errUploadBatchFailed && sp.err != context.Canceled && sp.err != context.DeadlineExceeded {
				t.Fatalf("failure standout span err = %v, not one of the fixed sentinel kinds: the raw error crossed the Tracer boundary", sp.err)
			}
		}
		for _, needle := range forbidden {
			if strings.Contains(sp.name, needle) {
				t.Fatalf("span name %q leaked %q", sp.name, needle)
			}
			if sp.err != nil && strings.Contains(sp.err.Error(), needle) {
				t.Fatalf("span %q err leaked %q", sp.name, needle)
			}
			for _, a := range sp.attrs {
				if strings.Contains(a.Value, needle) {
					t.Fatalf("span %q attribute %v leaked %q", sp.name, a, needle)
				}
			}
		}
	}
}

func attrValue(attrs []Attr, key deploymentsv1.AttributeKey) string {
	for _, a := range attrs {
		if a.Key == key {
			return a.Value
		}
	}
	return ""
}
