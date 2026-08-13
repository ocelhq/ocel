package deploy

import (
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
	s.record(uploadOutcome{Bytes: 100, Start: base, End: base.Add(10 * time.Millisecond)})
	s.record(uploadOutcome{Bytes: 250, Start: base.Add(time.Millisecond), End: base.Add(20 * time.Millisecond)})
	s.record(uploadOutcome{Bytes: 0, Start: base.Add(2 * time.Millisecond), End: base.Add(5 * time.Millisecond)})

	snap := s.snapshot()
	if snap.count != 3 {
		t.Fatalf("count = %d, want 3", snap.count)
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

func TestUploadBatchStatsRecordsFailuresUncapped(t *testing.T) {
	t.Parallel()

	s := newUploadBatchStats()
	base := time.Unix(2000, 0)
	for i := 0; i < maxUploadLatencyStandouts+5; i++ {
		s.record(uploadOutcome{Start: base, End: base.Add(time.Millisecond), Failed: true, Err: errors.New("boom")})
	}

	snap := s.snapshot()
	if len(snap.failures) != maxUploadLatencyStandouts+5 {
		t.Fatalf("len(failures) = %d, want %d: failures must never be dropped", len(snap.failures), maxUploadLatencyStandouts+5)
	}
	if snap.dropped != 0 {
		t.Fatalf("dropped = %d, want 0 (only latency standouts are capped)", snap.dropped)
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
	if snap.dropped != 5 {
		t.Fatalf("dropped = %d, want 5", snap.dropped)
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
				Bytes:  int64(i),
				Start:  base,
				End:    base.Add(dur),
				Failed: failed,
				Err:    err,
			})
		}(i)
	}
	wg.Wait()

	snap := s.snapshot()
	if snap.count != n {
		t.Fatalf("count = %d, want %d", snap.count, n)
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
	s.record(uploadOutcome{Bytes: 100, Start: base, End: base.Add(time.Millisecond)})
	s.record(uploadOutcome{Bytes: 200, Start: base.Add(time.Millisecond), End: base.Add(2 * time.Millisecond)})

	parent := NewRootStage("Uploading")
	emitUploadBatch(ft, parent.ID, uploadKindStaticAsset, s, nil)

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

func TestEmitUploadBatchSkipsWhenNothingHappened(t *testing.T) {
	t.Parallel()

	ft := &fakeTracer{}
	emitUploadBatch(ft, NewRootStage("Uploading").ID, uploadKindPrerenderAsset, newUploadBatchStats(), nil)
	if len(ft.spans) != 0 {
		t.Fatalf("got %d spans, want 0 for an empty batch", len(ft.spans))
	}
}

func TestEmitUploadBatchGivesEveryFailureItsOwnSpanUncapped(t *testing.T) {
	t.Parallel()

	ft := &fakeTracer{}
	s := newUploadBatchStats()
	base := time.Unix(7000, 0)
	const failures = maxUploadLatencyStandouts + 3
	for i := 0; i < failures; i++ {
		s.record(uploadOutcome{Start: base, End: base.Add(time.Millisecond), Failed: true, Err: errors.New("access denied for bucket x")})
	}

	parent := NewRootStage("Uploading")
	emitUploadBatch(ft, parent.ID, uploadKindFunctionArtifact, s, nil)

	failureSpans := 0
	for _, sp := range ft.spans {
		if sp.name == uploadStandoutName(uploadKindFunctionArtifact, true) {
			failureSpans++
			if sp.err == nil {
				t.Error("failure standout span has no error")
			}
		}
	}
	if failureSpans != failures {
		t.Fatalf("failure spans = %d, want %d: failures must never be capped", failureSpans, failures)
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

	emitUploadBatch(ft, NewRootStage("Uploading").ID, uploadKindPrerenderAsset, s, nil)

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

	emitUploadBatch(ft, NewRootStage("Uploading").ID, uploadKindStaticAsset, s, nil)

	for _, sp := range ft.spans {
		if sp.name == uploadBatchSpanName(uploadKindStaticAsset) && sp.err == nil {
			t.Fatal("batch span err = nil, want non-nil: the batch had a failure")
		}
	}
}

func TestUploadStandoutNameIsBoundedNeverDynamic(t *testing.T) {
	t.Parallel()

	seen := map[string]bool{}
	for _, k := range []uploadKind{uploadKindFunctionArtifact, uploadKindStaticAsset, uploadKindPrerenderAsset} {
		for _, failed := range []bool{true, false} {
			name := uploadStandoutName(k, failed)
			if name == "" {
				t.Errorf("uploadStandoutName(%d, %v) = empty", k, failed)
			}
			seen[name] = true
		}
	}
	if len(seen) != 6 {
		t.Errorf("uploadStandoutName produced %d distinct strings, want exactly 6 (3 kinds x pass/fail)", len(seen))
	}
}

func TestNoFreeFormTextReachesAnUploadSpan(t *testing.T) {
	t.Parallel()

	const (
		secretBucket = "my-super-secret-bucket-name"
		secretKey    = "assets/proj/web/RELEASE123/logo-with-pii.png"
		secretPath   = "/home/build/.ocel/tmp/RELEASE123/logo-with-pii.png"
		secretURL    = "https://my-super-secret-bucket-name.s3.amazonaws.com/assets/proj/web/RELEASE123/logo-with-pii.png?X-Amz-Signature=abc123"
	)
	rawErr := fmt.Errorf("upload artifact %s/%s: dial %s: connection refused (path %s)", secretBucket, secretKey, secretURL, secretPath)

	ft := &fakeTracer{}
	s := newUploadBatchStats()
	base := time.Unix(10000, 0)
	s.record(uploadOutcome{Start: base, End: base.Add(time.Millisecond), Failed: true, Err: rawErr})
	s.record(uploadOutcome{Start: base, End: base.Add(uploadLatencyOutlierThreshold + time.Millisecond)})

	emitUploadBatch(ft, NewRootStage("Uploading").ID, uploadKindFunctionArtifact, s, rawErr)

	forbidden := []string{secretBucket, secretKey, secretPath, secretURL}
	for _, sp := range ft.spans {
		for _, needle := range forbidden {
			if strings.Contains(sp.name, needle) {
				t.Fatalf("span name %q leaked %q", sp.name, needle)
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
