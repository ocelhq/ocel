package deploy

import (
	"errors"
	"sync"
	"time"
)

const (
	uploadLatencyOutlierThreshold = 2 * time.Second
	maxUploadLatencyStandouts     = 20
)

var errUploadBatchFailed = errors.New("upload batch failed")

type uploadKind int

const (
	uploadKindFunctionArtifact uploadKind = iota
	uploadKindStaticAsset
	uploadKindPrerenderAsset
)

func uploadBatchSpanName(k uploadKind) string {
	switch k {
	case uploadKindFunctionArtifact:
		return "upload function artifacts"
	case uploadKindStaticAsset:
		return "upload static assets"
	case uploadKindPrerenderAsset:
		return "upload prerender assets"
	default:
		return "upload batch"
	}
}

func uploadStandoutName(k uploadKind, failed bool) string {
	switch k {
	case uploadKindFunctionArtifact:
		if failed {
			return "function artifact upload failed"
		}
		return "slow function artifact upload"
	case uploadKindStaticAsset:
		if failed {
			return "static asset upload failed"
		}
		return "slow static asset upload"
	case uploadKindPrerenderAsset:
		if failed {
			return "prerender asset upload failed"
		}
		return "slow prerender asset upload"
	default:
		if failed {
			return "upload failed"
		}
		return "slow upload"
	}
}

type uploadOutcome struct {
	Bytes  int64
	Start  time.Time
	End    time.Time
	Failed bool
	Err    error
}

type uploadBatchStats struct {
	mu sync.Mutex

	count int
	bytes int64
	start time.Time
	end   time.Time

	failures []uploadOutcome
	slowest  []uploadOutcome
	dropped  int
}

func newUploadBatchStats() *uploadBatchStats {
	return &uploadBatchStats{}
}

func (s *uploadBatchStats) record(o uploadOutcome) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.count++
	s.bytes += o.Bytes
	if s.start.IsZero() || o.Start.Before(s.start) {
		s.start = o.Start
	}
	if o.End.After(s.end) {
		s.end = o.End
	}

	if o.Failed {
		s.failures = append(s.failures, o)
		return
	}
	if uploadLatencyOutlierThreshold > 0 && o.End.Sub(o.Start) >= uploadLatencyOutlierThreshold {
		s.keepSlowestLocked(o)
	}
}

func (s *uploadBatchStats) keepSlowestLocked(o uploadOutcome) {
	if len(s.slowest) < maxUploadLatencyStandouts {
		s.slowest = append(s.slowest, o)
		return
	}
	minIdx, minDur := 0, s.slowest[0].End.Sub(s.slowest[0].Start)
	for i := 1; i < len(s.slowest); i++ {
		if d := s.slowest[i].End.Sub(s.slowest[i].Start); d < minDur {
			minIdx, minDur = i, d
		}
	}
	s.dropped++
	if d := o.End.Sub(o.Start); d > minDur {
		s.slowest[minIdx] = o
	}
}

type uploadBatchSnapshot struct {
	count    int
	bytes    int64
	start    time.Time
	end      time.Time
	failures []uploadOutcome
	slowest  []uploadOutcome
	dropped  int
}

func (s *uploadBatchStats) snapshot() uploadBatchSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return uploadBatchSnapshot{
		count:    s.count,
		bytes:    s.bytes,
		start:    s.start,
		end:      s.end,
		failures: append([]uploadOutcome(nil), s.failures...),
		slowest:  append([]uploadOutcome(nil), s.slowest...),
		dropped:  s.dropped,
	}
}

func emitUploadBatch(t Tracer, parent StageID, k uploadKind, stats *uploadBatchStats, phaseErr error) {
	if t == nil || stats == nil {
		return
	}
	snap := stats.snapshot()
	if snap.count == 0 && phaseErr == nil {
		return
	}

	batchErr := phaseErr
	if batchErr == nil && len(snap.failures) > 0 {
		batchErr = errUploadBatchFailed
	}
	spanUnder(t, parent, uploadBatchSpanName(k), snap.start, snap.end, batchErr, AttrResourceCount(snap.count), AttrBytes(snap.bytes))

	for _, f := range snap.failures {
		spanUnder(t, parent, uploadStandoutName(k, true), f.Start, f.End, f.Err, AttrBytes(f.Bytes))
	}
	for _, s := range snap.slowest {
		spanUnder(t, parent, uploadStandoutName(k, false), s.Start, s.End, nil, AttrDurationMS(s.End.Sub(s.Start)), AttrBytes(s.Bytes))
	}
}
