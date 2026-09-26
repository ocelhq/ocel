package providerkit_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/fake"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/records"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	"github.com/ocelhq/ocel/pkg/providerkit/stackrecords"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type recorder struct {
	mu      sync.Mutex
	said    []string
	details []string
}

func (r *recorder) Say(message string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.said = append(r.said, message)
}

func (r *recorder) Detail(message string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.details = append(r.details, message)
}

func (r *recorder) Span(string, time.Time, time.Time, error, ...edge.Attr) {}

func (r *recorder) told() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(append(slices.Clone(r.said), r.details...), "\n")
}

func gated(t *testing.T, writer provider.WrittenBy) (providerkit.Gate, *fake.Provider) {
	t.Helper()

	p := fake.NewProvider(fake.Options{Region: "nowhere"})
	return providerkit.Gate{
		Bootstrap: p.FakeBootstrap(),
		Records:   p.Records(),
		WrittenBy: writer,
	}, p
}

func bootstrapped(t *testing.T, p *fake.Provider, class edge.Class, features ...string) {
	t.Helper()

	if err := p.FakeBootstrap().Apply(context.Background(), provider.BootstrapRequest{
		Class:    class,
		Features: features,
	}, nil); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
}

func TestStateReadsWhatTheVendorDescribes(t *testing.T) {
	t.Parallel()

	gate, p := gated(t, "2.0.0")
	bootstrapped(t, p, edge.ClassProduction, fake.FeatureCache)

	state, err := gate.State(context.Background(), edge.ClassProduction)
	if err != nil {
		t.Fatalf("State() error = %v", err)
	}
	if !state.Present {
		t.Fatal("State() reports no bootstrap where one was applied")
	}
	if want := []string{fake.FeatureCache}; !slices.Equal(state.Features, want) {
		t.Errorf("State().Features = %v, want %v", state.Features, want)
	}
	if state.Schema != provider.BootstrapSchema {
		t.Errorf("State().Schema = %d, want %d", state.Schema, provider.BootstrapSchema)
	}
	if state.WrittenBy != "1.0.0" {
		t.Errorf("State().WrittenBy = %q, want the writer the core stack carries", state.WrittenBy)
	}
	if state.AutoHeal {
		t.Error("State().AutoHeal is on with no bootstrap record written")
	}
}

func TestStateReadsAutoHealFromTheRecord(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	gate, provider := gated(t, "2.0.0")
	bootstrapped(t, provider, edge.ClassProduction, fake.FeatureCache)

	if err := gate.RecordBootstrap(ctx, edge.ClassProduction, stackrecords.BootstrapSettings{AutoHeal: true}); err != nil {
		t.Fatalf("RecordBootstrap() error = %v", err)
	}
	state, err := gate.State(ctx, edge.ClassProduction)
	if err != nil {
		t.Fatalf("State() error = %v", err)
	}
	if !state.AutoHeal {
		t.Error("State().AutoHeal is off after the record said it is on")
	}

	held, err := provider.Records().Read(ctx, stackrecords.BootstrapRecord(edge.ClassProduction))
	if err != nil {
		t.Fatalf("Read() of the bootstrap record = %v", err)
	}
	var settings stackrecords.BootstrapSettings
	if err := json.Unmarshal(held.Bytes, &settings); err != nil || !settings.AutoHeal {
		t.Fatalf("the bootstrap record holds %q, %v, want auto_heal on", held.Bytes, err)
	}
}

func TestAdmitRefusesABootstrapThatIsNotThere(t *testing.T) {
	t.Parallel()

	gate, _ := gated(t, "2.0.0")

	_, err := gate.Admit(context.Background(), edge.ClassPreview, nil, true, &recorder{})
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeNotReady {
		t.Fatalf("Admit() = %v, want a %s refusal", err, refusal.CodeNotReady)
	}
	if !strings.Contains(refused.Message, "`ocel bootstrap preview`") {
		t.Errorf("Admit() = %q, want it to name the command that creates the preview bootstrap", refused.Message)
	}
}

func TestAdmitRefusesASchemaThisBuildCannotRead(t *testing.T) {
	t.Parallel()

	gate, p := gated(t, "2.0.0")
	bootstrapped(t, p, edge.ClassProduction)
	p.FakeBootstrap().AtSchema(provider.BootstrapSchema + 1)

	_, err := gate.Admit(context.Background(), edge.ClassProduction, nil, true, &recorder{})
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeNotReady {
		t.Fatalf("Admit() = %v, want a %s refusal", err, refusal.CodeNotReady)
	}
	if !strings.Contains(refused.Message, "Upgrade the Ocel CLI") {
		t.Errorf("Admit() = %q, want it to say the CLI is behind the account", refused.Message)
	}
}

func TestAdmitRefusesAMissingFeatureAndOffersTheOneCommandThatAddsIt(t *testing.T) {
	t.Parallel()

	gate, provider := gated(t, "2.0.0")
	bootstrapped(t, provider, edge.ClassProduction, fake.FeatureCache)

	_, err := gate.Admit(context.Background(), edge.ClassProduction, []string{fake.FeatureImages}, true, &recorder{})
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeNotReady {
		t.Fatalf("Admit() = %v, want a %s refusal", err, refusal.CodeNotReady)
	}
	for _, want := range []string{
		"lacks the features this project needs: " + fake.FeatureImages,
		"`ocel bootstrap production --features " + fake.FeatureImages + "`",
	} {
		if !strings.Contains(refused.Message, want) {
			t.Errorf("Admit() = %q, want it to contain %q", refused.Message, want)
		}
	}
}

func TestAdmitHealsAStaleBootstrapUnattended(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	gate, provider := gated(t, "2.0.0")
	bootstrap := provider.FakeBootstrap()
	bootstrapped(t, provider, edge.ClassProduction, fake.FeatureCache)
	if err := gate.RecordBootstrap(ctx, edge.ClassProduction, stackrecords.BootstrapSettings{AutoHeal: true}); err != nil {
		t.Fatal(err)
	}
	bootstrap.Behind(fake.FeatureCache)

	progress := &recorder{}
	state, err := gate.Admit(ctx, edge.ClassProduction, []string{fake.FeatureCache}, true, progress)
	if err != nil {
		t.Fatalf("Admit() error = %v", err)
	}
	if stale := state.Stale([]string{fake.FeatureCache}); len(stale) != 0 {
		t.Errorf("Admit() left %v behind, want the heal to have refreshed them", stale)
	}

	applied := bootstrap.Applied()
	healing := applied[len(applied)-1]
	if !healing.Unattended {
		t.Error("the heal reached Apply() attended, and nothing is there to accept a replacement")
	}
	if !slices.Equal(healing.Features, []string{fake.FeatureCache}) {
		t.Errorf("the heal applied %v, want the features already state", healing.Features)
	}
}

func TestAdmitLeavesAStaleBootstrapAloneWhenTheAccountNeverOptedIntoHealing(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	gate, provider := gated(t, "2.0.0")
	bootstrap := provider.FakeBootstrap()
	bootstrapped(t, provider, edge.ClassProduction, fake.FeatureCache)
	bootstrap.Behind(fake.FeatureCache)

	progress := &recorder{}
	if _, err := gate.Admit(ctx, edge.ClassProduction, []string{fake.FeatureCache}, true, progress); err != nil {
		t.Fatalf("Admit() error = %v", err)
	}
	if got := len(bootstrap.Applied()); got != 1 {
		t.Errorf("Apply() ran %d times, want only the bootstrap that stood it up", got)
	}
	if !strings.Contains(progress.told(), "its content is behind") {
		t.Errorf("Admit() said %q, want it to report the drift it left state", progress.told())
	}
}

func TestAdmitAsksForNoHealingAndGetsNone(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	gate, provider := gated(t, "2.0.0")
	bootstrap := provider.FakeBootstrap()
	bootstrapped(t, provider, edge.ClassProduction, fake.FeatureCache)
	if err := gate.RecordBootstrap(ctx, edge.ClassProduction, stackrecords.BootstrapSettings{AutoHeal: true}); err != nil {
		t.Fatal(err)
	}
	bootstrap.Behind(fake.FeatureCache)

	progress := &recorder{}
	state, err := gate.Admit(ctx, edge.ClassProduction, []string{fake.FeatureCache}, false, progress)
	if err != nil {
		t.Fatalf("Admit() error = %v", err)
	}
	if got := len(bootstrap.Applied()); got != 1 {
		t.Errorf("Apply() ran %d times, want a caller that asked for no healing to get none though the account opted in", got)
	}
	if stale := state.Stale([]string{fake.FeatureCache}); len(stale) != 1 {
		t.Errorf("Admit() reports %v behind, want the drift it was told to leave state", stale)
	}
	if !strings.Contains(progress.told(), "its content is behind") {
		t.Errorf("Admit() said %q, want it to report the drift it left state", progress.told())
	}
}

func TestAdmitWillNotHealFromADevelopmentBuild(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	gate, provider := gated(t, "dev+cafebabe")
	bootstrap := provider.FakeBootstrap()
	bootstrapped(t, provider, edge.ClassProduction, fake.FeatureCache)
	if err := gate.RecordBootstrap(ctx, edge.ClassProduction, stackrecords.BootstrapSettings{AutoHeal: true}); err != nil {
		t.Fatal(err)
	}
	bootstrap.Behind(fake.FeatureCache)

	progress := &recorder{}
	if _, err := gate.Admit(ctx, edge.ClassProduction, []string{fake.FeatureCache}, true, progress); err != nil {
		t.Fatalf("Admit() error = %v", err)
	}
	if got := len(bootstrap.Applied()); got != 1 {
		t.Errorf("Apply() ran %d times, want a development build to leave the account as it stands", got)
	}
	if !strings.Contains(progress.told(), "development build (dev+cafebabe)") {
		t.Errorf("Admit() said %q, want it to name the build that declined to heal", progress.told())
	}
}

func TestAdmitReportsAHealTheCredentialsCannotDo(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	gate, provider := gated(t, "2.0.0")
	bootstrap := provider.FakeBootstrap()
	bootstrapped(t, provider, edge.ClassProduction, fake.FeatureCache)
	if err := gate.RecordBootstrap(ctx, edge.ClassProduction, stackrecords.BootstrapSettings{AutoHeal: true}); err != nil {
		t.Fatal(err)
	}
	bootstrap.Behind(fake.FeatureCache)
	bootstrap.RefuseApply(refusal.Refuse(refusal.CodeDenied,
		"ocel-deploy@10.0.0.4 can neither act as root nor run sudo without a password"))

	progress := &recorder{}
	if _, err := gate.Admit(ctx, edge.ClassProduction, []string{fake.FeatureCache}, true, progress); err != nil {
		t.Fatalf("Admit() error = %v, want a refused heal to leave the run state", err)
	}
	if !strings.Contains(progress.told(), "ocel-deploy@10.0.0.4 can neither act as root nor run sudo without a password") {
		t.Errorf("Admit() said %q, want the provider's own account of why the heal was denied", progress.told())
	}
	kit, _, _ := strings.Cut(refusedLine(t, progress), ": ")
	for _, vendored := range []string{"account", "stack"} {
		if strings.Contains(kit, vendored) {
			t.Errorf("the kit wrote %q, and it speaks %q at a host that has no such thing", kit, vendored)
		}
	}
}

func refusedLine(t *testing.T, progress *recorder) string {
	t.Helper()

	for _, line := range strings.Split(progress.told(), "\n") {
		if strings.HasPrefix(line, "this run may not refresh") {
			return line
		}
	}
	t.Fatalf("nothing in %q says the heal was denied", progress.told())
	return ""
}

func TestADeniedHealWithNothingToSayStillReadsAsASentence(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	gate, provider := gated(t, "2.0.0")
	bootstrap := provider.FakeBootstrap()
	bootstrapped(t, provider, edge.ClassProduction, fake.FeatureCache)
	if err := gate.RecordBootstrap(ctx, edge.ClassProduction, stackrecords.BootstrapSettings{AutoHeal: true}); err != nil {
		t.Fatal(err)
	}
	bootstrap.Behind(fake.FeatureCache)
	bootstrap.RefuseApply(refusal.Refuse(refusal.CodeDenied, ""))

	progress := &recorder{}
	if _, err := gate.Admit(ctx, edge.ClassProduction, []string{fake.FeatureCache}, true, progress); err != nil {
		t.Fatalf("Admit() error = %v, want a refused heal to leave the run state", err)
	}
	if line := refusedLine(t, progress); strings.Contains(line, ": ") {
		t.Errorf("the kit wrote %q, want no colon introducing a reason the provider never gave", line)
	}
}

func TestAnApplyThatNeverFinishedReachesTheCLIAsOneAndReadsAsDrifted(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	gate, provider := gated(t, "2.0.0")
	class := edge.ClassProduction
	bootstrapped(t, provider, class, fake.FeatureCache)
	provider.FakeBootstrap().Halfway()

	state, err := gate.State(ctx, class)
	if err != nil {
		t.Fatal(err)
	}
	if !state.Unfinished {
		t.Fatal("State() reads a half-applied bootstrap as one that finished, so nothing downstream can banner it")
	}
	status := providerkit.BootstrapStatusProto(state, "2.0.0", environmentv1.Tier_TIER_PRODUCTION, state.Features)
	if !status.GetUnfinished() {
		t.Error("the status the CLI is handed says nothing about the apply that never finished")
	}
}

func TestDowngradeIsAWriterOlderThanTheOneThatWrote(t *testing.T) {
	t.Parallel()

	gate, provider := gated(t, "1.0.0")
	bootstrapped(t, provider, edge.ClassProduction)
	provider.FakeBootstrap().WrittenBy("2.0.0")

	state, err := gate.State(context.Background(), edge.ClassProduction)
	if err != nil {
		t.Fatalf("State() error = %v", err)
	}
	if !state.Downgrade("1.0.0") {
		t.Error("Downgrade() = false where a newer build wrote the bootstrap this one is about to write")
	}
	if state.Downgrade("3.0.0") {
		t.Error("Downgrade() = true where the build about to write is the newer one")
	}
}

func TestOccupancyRefusesWhileAnythingStandsOnTheBootstrap(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	gate, provider := gated(t, "2.0.0")

	occupancy, err := gate.Occupancy(ctx, edge.ClassPreview)
	if err != nil {
		t.Fatalf("Occupancy() error = %v", err)
	}
	if err := occupancy.Refuse(edge.ClassPreview); err != nil {
		t.Fatalf("Refuse() over an empty account = %v, want nothing in the way", err)
	}

	for _, slug := range []string{"shop", "blog"} {
		if _, err := provider.Records().Write(ctx, records.Record{Name: stackrecords.ProjectRecord(edge.ClassPreview, slug)}); err != nil {
			t.Fatal(err)
		}
	}
	wildcard, err := json.Marshal(stackrecords.Wildcard{BaseDomain: "previews.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Records().Write(ctx, records.Record{
		Name:  stackrecords.WildcardRecord(edge.ClassPreview),
		Bytes: wildcard,
	}); err != nil {
		t.Fatal(err)
	}

	occupancy, err = gate.Occupancy(ctx, edge.ClassPreview)
	if err != nil {
		t.Fatalf("Occupancy() error = %v", err)
	}
	if !slices.Equal(occupancy.Projects, []string{"blog", "shop"}) {
		t.Errorf("Occupancy().Projects = %v, want both projects, sorted", occupancy.Projects)
	}
	var refused refusal.Refusal
	err = occupancy.Refuse(edge.ClassPreview)
	if !errors.As(err, &refused) || refused.Code != refusal.CodeNotReady {
		t.Fatalf("Refuse() = %v, want a %s refusal", err, refusal.CodeNotReady)
	}
	for _, want := range []string{
		"2 project(s) are still deployed into it: blog, shop",
		"`ocel destroy preview`",
		"*.previews.example.com",
		"`ocel bootstrap destroy preview`",
	} {
		if !strings.Contains(refused.Message, want) {
			t.Errorf("Refuse() = %q, want it to contain %q", refused.Message, want)
		}
	}
}

func TestASchemaNewerThanThisBuildIsRefusedWithNoEscapeHatch(t *testing.T) {
	t.Parallel()

	err := providerkit.RefuseSchemaAhead(provider.BootstrapSchema+1, true, edge.ClassProduction)
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeNotReady {
		t.Fatalf("RefuseSchemaAhead() = %v, want a %s refusal", err, refusal.CodeNotReady)
	}
	if !strings.Contains(refused.Message, "ocel bootstrap destroy") {
		t.Errorf("RefuseSchemaAhead() = %q, want it to name what drops the bootstrap", refused.Message)
	}
	if strings.Contains(refused.Message, "--force") {
		t.Errorf("RefuseSchemaAhead() = %q, want no escape hatch offered", refused.Message)
	}
	if got := strings.Count(refused.Message, "\n"); got != 1 {
		t.Errorf("RefuseSchemaAhead() = %q, want exactly two lines", refused.Message)
	}
	if err := providerkit.RefuseSchemaAhead(provider.BootstrapSchema, true, edge.ClassProduction); err != nil {
		t.Errorf("RefuseSchemaAhead() at the schema this build writes = %v, want it admitted", err)
	}
}

func TestAPreviewBootstrapIsRemediatedWithItsOwnCommand(t *testing.T) {
	t.Parallel()

	gate, provider := gated(t, "2.0.0")
	bootstrapped(t, provider, edge.ClassPreview)

	_, err := gate.Admit(context.Background(), edge.ClassPreview, []string{fake.FeatureImages}, true, &recorder{})
	var refusal refusal.Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("Admit() = %v, want a refusal", err)
	}
	if !strings.Contains(refusal.Message, "`ocel bootstrap preview --features ") {
		t.Errorf("Admit() = %q, want the preview bootstrap's own command", refusal.Message)
	}
}
