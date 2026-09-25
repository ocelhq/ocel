package pulumi

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pulumi/pulumi/sdk/v3/go/auto/events"
	"github.com/pulumi/pulumi/sdk/v3/go/common/apitype"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

type recordedSpan struct {
	name  string
	err   error
	attrs []providerkit.Attr
}

type fakeProgress struct {
	said    []string
	details []string
	spans   []recordedSpan
}

func (r *fakeProgress) Say(message string) { r.said = append(r.said, message) }

func (r *fakeProgress) Detail(message string) { r.details = append(r.details, message) }

func (r *fakeProgress) Span(name string, _, _ time.Time, err error, attrs ...providerkit.Attr) {
	r.spans = append(r.spans, recordedSpan{name: name, err: err, attrs: attrs})
}

func TestTheBatchSpanCarriesNoResourceIdentityAndTheStandoutDoes(t *testing.T) {
	t.Parallel()

	progress := &fakeProgress{}
	start := time.Unix(6000, 0)
	reportTrace(progress, engineTrace{
		ResourceCount: 2,
		Start:         start,
		End:           start.Add(5 * time.Second),
		Failed:        true,
		Standouts: []standout{{
			Op:     apitype.OpCreate,
			Type:   "aws:s3/bucket:Bucket",
			Name:   "my-bucket",
			Start:  start,
			End:    start.Add(5 * time.Second),
			Failed: true,
		}},
	}, nil)

	if len(progress.spans) != 2 {
		t.Fatalf("got %d spans, want 2 (batch + standout)", len(progress.spans))
	}

	batch := progress.spans[0]
	if batch.name != engineBatchSpanName {
		t.Fatalf("spans[0].name = %q, want the batch span name", batch.name)
	}
	for _, a := range batch.attrs {
		if a.Key == providerkit.AttrKeyResourceType || a.Key == providerkit.AttrKeyResourceName {
			t.Errorf("batch span carries resource identity attr %+v; it covers many resources", a)
		}
	}

	var sawType, sawName bool
	for _, a := range progress.spans[1].attrs {
		switch a.Key {
		case providerkit.AttrKeyResourceType:
			sawType = true
			if a.Value != "aws:s3/bucket:Bucket" {
				t.Errorf("RESOURCE_TYPE = %q, want the type token", a.Value)
			}
		case providerkit.AttrKeyResourceName:
			sawName = true
			if a.Value != "my-bucket" {
				t.Errorf("RESOURCE_NAME = %q, want the logical name", a.Value)
			}
			if strings.Contains(a.Value, "urn:pulumi") {
				t.Fatal("RESOURCE_NAME carried the raw URN")
			}
		}
	}
	if !sawType {
		t.Error("standout span is missing ATTRIBUTE_KEY_RESOURCE_TYPE")
	}
	if !sawName {
		t.Error("standout span is missing ATTRIBUTE_KEY_RESOURCE_NAME")
	}
}

func TestAStandoutWhoseURNDidNotParseCarriesNoResourceIdentity(t *testing.T) {
	t.Parallel()

	progress := &fakeProgress{}
	start := time.Unix(7000, 0)
	reportTrace(progress, engineTrace{
		ResourceCount: 1,
		Start:         start,
		End:           start.Add(time.Second),
		Failed:        true,
		Standouts: []standout{
			{Op: apitype.OpCreate, Start: start, End: start.Add(time.Second), Failed: true},
		},
	}, nil)

	if len(progress.spans) != 2 {
		t.Fatalf("got %d spans, want 2", len(progress.spans))
	}
	for _, a := range progress.spans[1].attrs {
		if a.Key == providerkit.AttrKeyResourceType || a.Key == providerkit.AttrKeyResourceName {
			t.Errorf("standout span carries resource identity attr %+v despite an unparseable URN", a)
		}
	}
}

func TestARunThatFailedBeforeTouchingAResourceStillLeavesASpan(t *testing.T) {
	t.Parallel()

	progress := &fakeProgress{}
	start := time.Unix(8000, 0)
	reportTrace(progress, engineTrace{Start: start, End: start.Add(time.Second)}, errors.New("plugin failed to start"))

	if len(progress.spans) != 1 {
		t.Fatalf("got %d spans, want 1", len(progress.spans))
	}
	if progress.spans[0].err == nil {
		t.Error("batch span not recorded as failed")
	}
}

func TestAQuietSuccessfulRunSaysNothing(t *testing.T) {
	t.Parallel()

	progress := &fakeProgress{}
	reportTrace(progress, engineTrace{}, nil)

	if len(progress.spans) != 0 {
		t.Fatalf("got %d spans, want 0: nothing happened and nothing failed", len(progress.spans))
	}
}

func TestATraceThatNeverArrivesReturnsWithinItsGrace(t *testing.T) {
	t.Parallel()

	result := make(chan engineTrace)
	done := make(chan engineTrace, 1)
	go func() { done <- awaitTrace(result, 20*time.Millisecond) }()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("awaitTrace did not return within its grace period")
	}
}

func TestAnEventStreamThatIsNeverClosedLeavesTheRunToFinish(t *testing.T) {
	t.Parallel()

	engineEvents := make(chan events.EngineEvent, 4)
	engineEvents <- events.EngineEvent{}

	trace := awaitTrace(drainTrace(engineEvents, 0), 50*time.Millisecond)
	if !reflect.DeepEqual(trace, engineTrace{}) {
		t.Errorf("got %+v, want a zero-value trace: the channel is unclosed so the builder goroutine never sent a result", trace)
	}
}

func TestTheEngineLogIsForwardedALineAtATime(t *testing.T) {
	t.Parallel()

	progress := &fakeProgress{}
	lines := detailWriter(progress)
	if _, err := lines.Write([]byte("creating bucket\r\nupdating role\npart")); err != nil {
		t.Fatal(err)
	}
	if want := []string{"creating bucket", "updating role"}; !reflect.DeepEqual(progress.details, want) {
		t.Fatalf("forwarded %q, want %q", progress.details, want)
	}
	lines.Flush()
	if len(progress.details) != 3 || progress.details[2] != "part" {
		t.Errorf("forwarded %q, want the trailing partial line flushed", progress.details)
	}
}
