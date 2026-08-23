package pulumi

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/pulumi/pulumi/sdk/v3/go/auto/events"
	"github.com/pulumi/pulumi/sdk/v3/go/common/apitype"
)

func preEvent(urn, typ string) events.EngineEvent {
	return events.EngineEvent{EngineEvent: apitype.EngineEvent{
		ResourcePreEvent: &apitype.ResourcePreEvent{
			Metadata: apitype.StepEventMetadata{URN: urn, Type: typ, Op: apitype.OpCreate},
		},
	}}
}

func outputsEvent(urn, typ string, op apitype.OpType) events.EngineEvent {
	return events.EngineEvent{EngineEvent: apitype.EngineEvent{
		ResOutputsEvent: &apitype.ResOutputsEvent{
			Metadata: apitype.StepEventMetadata{URN: urn, Type: typ, Op: op},
		},
	}}
}

func failedEvent(urn, typ string, op apitype.OpType) events.EngineEvent {
	return events.EngineEvent{EngineEvent: apitype.EngineEvent{
		ResOpFailedEvent: &apitype.ResOpFailedEvent{
			Metadata: apitype.StepEventMetadata{URN: urn, Type: typ, Op: op},
		},
	}}
}

const testURN = "urn:pulumi:prod::proj::aws:s3/bucket:Bucket::my-bucket"

func TestEngineTraceBuilderCountsResourceOperations(t *testing.T) {
	t.Parallel()

	b := newTraceBuilder(0)
	base := time.Unix(1000, 0)
	b.consume(preEvent(testURN, "aws:s3/bucket:Bucket"), base)
	b.consume(outputsEvent(testURN, "aws:s3/bucket:Bucket", apitype.OpCreate), base.Add(time.Second))
	b.consume(preEvent("urn:pulumi:prod::proj::aws:iam/role:Role::my-role", "aws:iam/role:Role"), base.Add(2*time.Second))
	b.consume(outputsEvent("urn:pulumi:prod::proj::aws:iam/role:Role::my-role", "aws:iam/role:Role", apitype.OpUpdate), base.Add(3*time.Second))

	got := b.result()
	if got.ResourceCount != 2 {
		t.Fatalf("ResourceCount = %d, want 2", got.ResourceCount)
	}
	if got.Failed {
		t.Fatal("Failed = true, want false")
	}
	if !got.Start.Equal(base) || !got.End.Equal(base.Add(3*time.Second)) {
		t.Fatalf("Start/End = %v/%v, want %v/%v", got.Start, got.End, base, base.Add(3*time.Second))
	}
}

func TestEngineTraceBuilderRecordsAFailureAsAStandout(t *testing.T) {
	t.Parallel()

	b := newTraceBuilder(0)
	base := time.Unix(2000, 0)
	b.consume(preEvent(testURN, "aws:s3/bucket:Bucket"), base)
	b.consume(failedEvent(testURN, "aws:s3/bucket:Bucket", apitype.OpCreate), base.Add(5*time.Second))

	got := b.result()
	if !got.Failed {
		t.Fatal("Failed = false, want true")
	}
	if len(got.Standouts) != 1 {
		t.Fatalf("len(Standouts) = %d, want 1", len(got.Standouts))
	}
	s := got.Standouts[0]
	if !s.Failed {
		t.Error("Standouts[0].Failed = false, want true")
	}
	if !s.Start.Equal(base) || !s.End.Equal(base.Add(5*time.Second)) {
		t.Errorf("Standouts[0] Start/End = %v/%v, want %v/%v", s.Start, s.End, base, base.Add(5*time.Second))
	}
}

func TestEngineTraceBuilderRecordsALatencyOutlier(t *testing.T) {
	t.Parallel()

	b := newTraceBuilder(2 * time.Second)
	base := time.Unix(3000, 0)
	b.consume(preEvent(testURN, "aws:s3/bucket:Bucket"), base)
	b.consume(outputsEvent(testURN, "aws:s3/bucket:Bucket", apitype.OpCreate), base.Add(10*time.Second))

	got := b.result()
	if len(got.Standouts) != 1 {
		t.Fatalf("len(Standouts) = %d, want 1 (operation exceeded the outlier threshold)", len(got.Standouts))
	}
	if got.Standouts[0].Failed {
		t.Error("Standouts[0].Failed = true, want false: a slow success is an outlier, not a failure")
	}
}

func TestEngineTraceBuilderIgnoresFastOperationsUnderThreshold(t *testing.T) {
	t.Parallel()

	b := newTraceBuilder(2 * time.Second)
	base := time.Unix(4000, 0)
	b.consume(preEvent(testURN, "aws:s3/bucket:Bucket"), base)
	b.consume(outputsEvent(testURN, "aws:s3/bucket:Bucket", apitype.OpCreate), base.Add(500*time.Millisecond))

	if got := b.result(); len(got.Standouts) != 0 {
		t.Fatalf("len(Standouts) = %d, want 0", got.ResourceCount)
	}
}

func TestEngineTraceBuilderNeverRetainsTheURN(t *testing.T) {
	t.Parallel()

	b := newTraceBuilder(0)
	base := time.Unix(5000, 0)
	b.consume(preEvent(testURN, "aws:s3/bucket:Bucket"), base)
	b.consume(failedEvent(testURN, "aws:s3/bucket:Bucket", apitype.OpCreate), base.Add(time.Second))

	got := b.result()
	if len(got.Standouts) != 1 {
		t.Fatalf("len(Standouts) = %d, want 1", len(got.Standouts))
	}
	s := got.Standouts[0]
	if s.Op == "" || s.Type == "" || s.Name == "" {
		t.Fatalf("standout missing Op/Type/Name: %+v", s)
	}
	if s.Type != "aws:s3/bucket:Bucket" {
		t.Errorf("Type = %q, want the type token", s.Type)
	}
	if s.Name != "my-bucket" {
		t.Errorf("Name = %q, want the URN's logical name component", s.Name)
	}
	if strings.Contains(s.Type, "prod") || strings.Contains(s.Type, "proj") {
		t.Fatalf("Type leaked the URN's stack or project: %q", s.Type)
	}
	if strings.Contains(s.Name, "prod") || strings.Contains(s.Name, "proj") {
		t.Fatalf("Name leaked the URN's stack or project: %q", s.Name)
	}
}

func TestResourceNameFromURNParsesDefensively(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		urn  string
		want string
	}{
		{"well-formed", testURN, "my-bucket"},
		{"empty", "", ""},
		{"not a urn at all", "not-a-urn", ""},
		{"missing the name component", "urn:pulumi:prod::proj::aws:s3/bucket:Bucket", ""},
		{"wrong namespace", "urn:notpulumi:prod::proj::aws:s3/bucket:Bucket::my-bucket", ""},
		{"name embeds the delimiter", "urn:pulumi:prod::proj::aws:s3/bucket:Bucket::weird::name", "weird::name"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := resourceNameFromURN(tc.urn); got != tc.want {
				t.Errorf("resourceNameFromURN(%q) = %q, want %q", tc.urn, got, tc.want)
			}
		})
	}
}

func TestCapIdentifierBoundsLength(t *testing.T) {
	t.Parallel()

	short := "aws:s3/bucket:Bucket"
	if got := capIdentifier(short); got != short {
		t.Errorf("capIdentifier(%q) = %q, want unchanged", short, got)
	}

	long := strings.Repeat("x", maxResourceIdentifierLen*2)
	got := capIdentifier(long)
	if len(got) != maxResourceIdentifierLen {
		t.Fatalf("capIdentifier(long) length = %d, want %d", len(got), maxResourceIdentifierLen)
	}

	multibyte := strings.Repeat("é", maxResourceIdentifierLen)
	got = capIdentifier(multibyte)
	if !utf8.ValidString(got) {
		t.Fatalf("capIdentifier(multibyte) = %q, not valid UTF-8", got)
	}
	if len(got) > maxResourceIdentifierLen {
		t.Fatalf("capIdentifier(multibyte) length = %d, want <= %d", len(got), maxResourceIdentifierLen)
	}
}

func TestEngineTraceBuilderCapsLatencyStandoutsKeepingTheSlowest(t *testing.T) {
	t.Parallel()

	b := newTraceBuilder(time.Second)
	base := time.Unix(6000, 0)
	for i := 0; i < maxLatencyStandouts+5; i++ {
		urn := "urn:pulumi:prod::proj::aws:s3/bucket:Bucket::bucket-" + strings.Repeat("x", i+1)
		dur := time.Duration(i+1) * time.Second
		b.consume(preEvent(urn, "aws:s3/bucket:Bucket"), base)
		b.consume(outputsEvent(urn, "aws:s3/bucket:Bucket", apitype.OpCreate), base.Add(dur))
	}

	got := b.result()
	if len(got.Standouts) != maxLatencyStandouts {
		t.Fatalf("len(Standouts) = %d, want %d", len(got.Standouts), maxLatencyStandouts)
	}
	if got.StandoutsDropped != 5 {
		t.Fatalf("StandoutsDropped = %d, want 5", got.StandoutsDropped)
	}
	for _, s := range got.Standouts {
		if s.End.Sub(s.Start) < 6*time.Second {
			t.Fatalf("kept a standout shorter than the evicted ones: %v", s.End.Sub(s.Start))
		}
	}
}

func TestEngineTraceBuilderNeverCapsFailures(t *testing.T) {
	t.Parallel()

	b := newTraceBuilder(time.Hour)
	base := time.Unix(7000, 0)
	for i := 0; i < maxLatencyStandouts+5; i++ {
		urn := "urn:pulumi:prod::proj::aws:s3/bucket:Bucket::bucket-" + strings.Repeat("x", i+1)
		b.consume(preEvent(urn, "aws:s3/bucket:Bucket"), base)
		b.consume(failedEvent(urn, "aws:s3/bucket:Bucket", apitype.OpCreate), base.Add(time.Millisecond))
	}

	got := b.result()
	if len(got.Standouts) != maxLatencyStandouts+5 {
		t.Fatalf("len(Standouts) = %d, want %d: failures must never be dropped", len(got.Standouts), maxLatencyStandouts+5)
	}
	if got.StandoutsDropped != 0 {
		t.Fatalf("StandoutsDropped = %d, want 0", got.StandoutsDropped)
	}
}

func TestResourceStandoutNameIsBoundedNeverDynamic(t *testing.T) {
	t.Parallel()

	cases := []apitype.OpType{
		apitype.OpCreate, apitype.OpUpdate, apitype.OpDelete, apitype.OpReplace,
		apitype.OpRead, apitype.OpRefresh, apitype.OpSame, apitype.OpImport,
	}
	seen := map[string]bool{}
	for _, op := range cases {
		name := standoutName(op, false)
		if name == "" {
			t.Errorf("standoutName(%s, false) = empty", op)
		}
		seen[name] = true
	}
	if name := standoutName(apitype.OpCreate, true); name != "resource operation failed" {
		t.Errorf("standoutName(create, true) = %q, want the fixed failure string", name)
	}
	if len(seen) > 7 {
		t.Errorf("standoutName produced %d distinct strings across %d ops; vocabulary should stay small and fixed", len(seen), len(cases))
	}
}
