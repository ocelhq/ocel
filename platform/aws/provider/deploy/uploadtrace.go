package deploy

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

const (
	uploadLatencyOutlierThreshold = 2 * time.Second
	maxUploadLatencyStandouts     = 20
	maxUploadFailureStandouts     = 20
)

var errUploadBatchFailed = errors.New("upload batch failed")

type uploadKind int

const (
	uploadKindFunctionArtifact uploadKind = iota
	uploadKindStaticAsset
	uploadKindPrerenderAsset
	uploadKindEdgeBundle
)

func uploadBatchSpanName(k uploadKind) string {
	switch k {
	case uploadKindFunctionArtifact:
		return "upload function artifacts"
	case uploadKindStaticAsset:
		return "upload static assets"
	case uploadKindPrerenderAsset:
		return "upload prerender assets"
	case uploadKindEdgeBundle:
		return "upload edge bundles"
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
	case uploadKindEdgeBundle:
		if failed {
			return "edge bundle upload failed"
		}
		return "slow edge bundle upload"
	default:
		if failed {
			return "upload failed"
		}
		return "slow upload"
	}
}

type uploadOutcome struct {
	Bytes       int64
	Start       time.Time
	End         time.Time
	Failed      bool
	Transferred bool
	Err         error
}

type recordedFailure struct {
	Bytes int64
	Start time.Time
	End   time.Time
	Kind  string
}

func errorForKind(kind string) error {
	switch kind {
	case ErrorKindCanceled:
		return context.Canceled
	case ErrorKindTimeout:
		return context.DeadlineExceeded
	default:
		return errUploadBatchFailed
	}
}

type uploadBatchStats struct {
	mu sync.Mutex

	attempts    int
	transferred int
	bytes       int64
	start       time.Time
	end         time.Time

	failures []recordedFailure
	slowest  []uploadOutcome
}

func newUploadBatchStats() *uploadBatchStats {
	return &uploadBatchStats{}
}

func (s *uploadBatchStats) record(o uploadOutcome) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.attempts++
	if o.Transferred {
		s.transferred++
	}
	s.bytes += o.Bytes
	if s.start.IsZero() || o.Start.Before(s.start) {
		s.start = o.Start
	}
	if o.End.After(s.end) {
		s.end = o.End
	}

	if o.Failed {
		kind := ClassifyError(o.Err)
		if kind == ErrorKindCanceled {
			return
		}
		if len(s.failures) < maxUploadFailureStandouts {
			s.failures = append(s.failures, recordedFailure{Bytes: o.Bytes, Start: o.Start, End: o.End, Kind: kind})
		}
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
	if d := o.End.Sub(o.Start); d > minDur {
		s.slowest[minIdx] = o
	}
}

type uploadBatchSnapshot struct {
	attempts    int
	transferred int
	bytes       int64
	start       time.Time
	end         time.Time
	failures    []recordedFailure
	slowest     []uploadOutcome
}

func (s *uploadBatchStats) snapshot() uploadBatchSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return uploadBatchSnapshot{
		attempts:    s.attempts,
		transferred: s.transferred,
		bytes:       s.bytes,
		start:       s.start,
		end:         s.end,
		failures:    append([]recordedFailure(nil), s.failures...),
		slowest:     append([]uploadOutcome(nil), s.slowest...),
	}
}

func emitUploadBatch(report providerkit.Reporter, k uploadKind, stats *uploadBatchStats, phaseErr error, phaseStart time.Time) {
	if report == nil || stats == nil {
		return
	}
	snap := stats.snapshot()
	if snap.attempts == 0 && phaseErr == nil {
		return
	}

	batchErr := phaseErr
	if batchErr == nil && len(snap.failures) > 0 {
		batchErr = errUploadBatchFailed
	}
	start, end := snap.start, snap.end
	if start.IsZero() {
		start = phaseStart
	}
	if end.IsZero() {
		end = phaseStart
	}
	report.Span(uploadBatchSpanName(k), start, end, batchErr,
		providerkit.AttrResourceCount(snap.transferred), providerkit.AttrBytes(snap.bytes))

	for _, f := range snap.failures {
		report.Span(uploadStandoutName(k, true), f.Start, f.End, errorForKind(f.Kind), providerkit.AttrBytes(f.Bytes))
	}
	for _, s := range snap.slowest {
		report.Span(uploadStandoutName(k, false), s.Start, s.End, nil,
			providerkit.AttrDurationMS(s.End.Sub(s.Start)), providerkit.AttrBytes(s.Bytes))
	}
}
