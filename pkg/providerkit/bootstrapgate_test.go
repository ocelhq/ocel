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

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/fake"
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

func (r *recorder) Span(string, time.Time, time.Time, error, ...providerkit.Attr) {}

func (r *recorder) told() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(append(slices.Clone(r.said), r.details...), "\n")
}

func gated(t *testing.T, writer providerkit.Writer) (providerkit.Gate, *fake.Provider) {
	t.Helper()

	provider := fake.NewProvider(fake.Options{Region: "nowhere"})
	return providerkit.Gate{
		Bootstrapper: provider.Bootstrapper(),
		Records:      provider.Records(),
		Writer:       writer,
	}, provider
}

func bootstrapped(t *testing.T, provider *fake.Provider, class providerkit.Class, features ...string) {
	t.Helper()

	if err := provider.Bootstrapper().Apply(context.Background(), providerkit.BootstrapRequest{
		Class:    class,
		Features: features,
	}, nil); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
}

func TestStandingReadsWhatTheVendorDescribes(t *testing.T) {
	t.Parallel()

	gate, provider := gated(t, "2.0.0")
	bootstrapped(t, provider, providerkit.ClassProduction, fake.FeatureCache)

	standing, err := gate.Standing(context.Background(), providerkit.ClassProduction)
	if err != nil {
		t.Fatalf("Standing() error = %v", err)
	}
	if !standing.Present {
		t.Fatal("Standing() reports no bootstrap where one was applied")
	}
	if want := []string{fake.FeatureCache}; !slices.Equal(standing.Features, want) {
		t.Errorf("Standing().Features = %v, want %v", standing.Features, want)
	}
	if standing.Schema != providerkit.BootstrapSchema {
		t.Errorf("Standing().Schema = %d, want %d", standing.Schema, providerkit.BootstrapSchema)
	}
	if standing.Writer != "1.0.0" {
		t.Errorf("Standing().Writer = %q, want the writer the core stack carries", standing.Writer)
	}
	if standing.AutoHeal {
		t.Error("Standing().AutoHeal is on with no bootstrap record written")
	}
}

func TestStandingReadsAutoHealFromTheRecord(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	gate, provider := gated(t, "2.0.0")
	bootstrapped(t, provider, providerkit.ClassProduction, fake.FeatureCache)

	if err := gate.RecordBootstrap(ctx, providerkit.ClassProduction, providerkit.BootstrapState{AutoHeal: true}); err != nil {
		t.Fatalf("RecordBootstrap() error = %v", err)
	}
	standing, err := gate.Standing(ctx, providerkit.ClassProduction)
	if err != nil {
		t.Fatalf("Standing() error = %v", err)
	}
	if !standing.AutoHeal {
		t.Error("Standing().AutoHeal is off after the record said it is on")
	}

	held, err := provider.Records().Read(ctx, providerkit.BootstrapRecord(providerkit.ClassProduction))
	if err != nil {
		t.Fatalf("Read() of the bootstrap record = %v", err)
	}
	var state providerkit.BootstrapState
	if err := json.Unmarshal(held.Bytes, &state); err != nil || !state.AutoHeal {
		t.Fatalf("the bootstrap record holds %q, %v, want auto_heal on", held.Bytes, err)
	}
}

func TestAdmitRefusesABootstrapThatIsNotThere(t *testing.T) {
	t.Parallel()

	gate, _ := gated(t, "2.0.0")

	_, err := gate.Admit(context.Background(), providerkit.ClassPreview, nil, &recorder{})
	var refusal providerkit.Refusal
	if !errors.As(err, &refusal) || refusal.Code != providerkit.CodeNotReady {
		t.Fatalf("Admit() = %v, want a %s refusal", err, providerkit.CodeNotReady)
	}
	if !strings.Contains(refusal.Message, "`ocel bootstrap preview`") {
		t.Errorf("Admit() = %q, want it to name the command that creates the preview bootstrap", refusal.Message)
	}
}

func TestAdmitRefusesASchemaThisBuildCannotRead(t *testing.T) {
	t.Parallel()

	gate, provider := gated(t, "2.0.0")
	bootstrapped(t, provider, providerkit.ClassProduction)
	provider.Bootstrapper().AtSchema(providerkit.BootstrapSchema + 1)

	_, err := gate.Admit(context.Background(), providerkit.ClassProduction, nil, &recorder{})
	var refusal providerkit.Refusal
	if !errors.As(err, &refusal) || refusal.Code != providerkit.CodeNotReady {
		t.Fatalf("Admit() = %v, want a %s refusal", err, providerkit.CodeNotReady)
	}
	if !strings.Contains(refusal.Message, "Upgrade the Ocel CLI") {
		t.Errorf("Admit() = %q, want it to say the CLI is behind the account", refusal.Message)
	}
}

func TestAdmitRefusesAMissingFeatureAndOffersTheOneCommandThatAddsIt(t *testing.T) {
	t.Parallel()

	gate, provider := gated(t, "2.0.0")
	bootstrapped(t, provider, providerkit.ClassProduction, fake.FeatureCache)

	_, err := gate.Admit(context.Background(), providerkit.ClassProduction, []string{fake.FeatureImages}, &recorder{})
	var refusal providerkit.Refusal
	if !errors.As(err, &refusal) || refusal.Code != providerkit.CodeNotReady {
		t.Fatalf("Admit() = %v, want a %s refusal", err, providerkit.CodeNotReady)
	}
	for _, want := range []string{
		"lacks the features this project needs: " + fake.FeatureImages,
		"`ocel bootstrap production --features " + fake.FeatureImages + "`",
	} {
		if !strings.Contains(refusal.Message, want) {
			t.Errorf("Admit() = %q, want it to contain %q", refusal.Message, want)
		}
	}
}

func TestAdmitHealsAStaleBootstrapUnattended(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	gate, provider := gated(t, "2.0.0")
	bootstrapper := provider.Bootstrapper()
	bootstrapped(t, provider, providerkit.ClassProduction, fake.FeatureCache)
	if err := gate.RecordBootstrap(ctx, providerkit.ClassProduction, providerkit.BootstrapState{AutoHeal: true}); err != nil {
		t.Fatal(err)
	}
	bootstrapper.Behind(fake.FeatureCache)

	report := &recorder{}
	standing, err := gate.Admit(ctx, providerkit.ClassProduction, []string{fake.FeatureCache}, report)
	if err != nil {
		t.Fatalf("Admit() error = %v", err)
	}
	if stale := standing.Stale([]string{fake.FeatureCache}); len(stale) != 0 {
		t.Errorf("Admit() left %v behind, want the heal to have refreshed them", stale)
	}

	applied := bootstrapper.Applied()
	healing := applied[len(applied)-1]
	if !healing.Unattended {
		t.Error("the heal reached Apply() attended, and nothing is there to accept a replacement")
	}
	if !slices.Equal(healing.Features, []string{fake.FeatureCache}) {
		t.Errorf("the heal applied %v, want the features already standing", healing.Features)
	}
}

func TestAdmitLeavesAStaleBootstrapAloneWhenNobodyAskedForHealing(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	gate, provider := gated(t, "2.0.0")
	bootstrapper := provider.Bootstrapper()
	bootstrapped(t, provider, providerkit.ClassProduction, fake.FeatureCache)
	bootstrapper.Behind(fake.FeatureCache)

	report := &recorder{}
	if _, err := gate.Admit(ctx, providerkit.ClassProduction, []string{fake.FeatureCache}, report); err != nil {
		t.Fatalf("Admit() error = %v", err)
	}
	if got := len(bootstrapper.Applied()); got != 1 {
		t.Errorf("Apply() ran %d times, want only the bootstrap that stood it up", got)
	}
	if !strings.Contains(report.told(), "its content is behind") {
		t.Errorf("Admit() said %q, want it to report the drift it left standing", report.told())
	}
}

func TestAdmitWillNotHealFromADevelopmentBuild(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	gate, provider := gated(t, "dev+cafebabe")
	bootstrapper := provider.Bootstrapper()
	bootstrapped(t, provider, providerkit.ClassProduction, fake.FeatureCache)
	if err := gate.RecordBootstrap(ctx, providerkit.ClassProduction, providerkit.BootstrapState{AutoHeal: true}); err != nil {
		t.Fatal(err)
	}
	bootstrapper.Behind(fake.FeatureCache)

	report := &recorder{}
	if _, err := gate.Admit(ctx, providerkit.ClassProduction, []string{fake.FeatureCache}, report); err != nil {
		t.Fatalf("Admit() error = %v", err)
	}
	if got := len(bootstrapper.Applied()); got != 1 {
		t.Errorf("Apply() ran %d times, want a development build to leave the account as it stands", got)
	}
	if !strings.Contains(report.told(), "development build (dev+cafebabe)") {
		t.Errorf("Admit() said %q, want it to name the build that declined to heal", report.told())
	}
}

func TestAdmitReportsAHealTheCredentialsCannotDo(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	gate, provider := gated(t, "2.0.0")
	bootstrapper := provider.Bootstrapper()
	bootstrapped(t, provider, providerkit.ClassProduction, fake.FeatureCache)
	if err := gate.RecordBootstrap(ctx, providerkit.ClassProduction, providerkit.BootstrapState{AutoHeal: true}); err != nil {
		t.Fatal(err)
	}
	bootstrapper.Behind(fake.FeatureCache)
	bootstrapper.RefuseApply(providerkit.Refuse(providerkit.CodeDenied,
		"ocel-deploy@10.0.0.4 can neither act as root nor run sudo without a password"))

	report := &recorder{}
	if _, err := gate.Admit(ctx, providerkit.ClassProduction, []string{fake.FeatureCache}, report); err != nil {
		t.Fatalf("Admit() error = %v, want a refused heal to leave the run standing", err)
	}
	if !strings.Contains(report.told(), "ocel-deploy@10.0.0.4 can neither act as root nor run sudo without a password") {
		t.Errorf("Admit() said %q, want the provider's own account of why the heal was denied", report.told())
	}
}

func TestDowngradeIsAWriterOlderThanTheOneThatWrote(t *testing.T) {
	t.Parallel()

	gate, provider := gated(t, "1.0.0")
	bootstrapped(t, provider, providerkit.ClassProduction)
	provider.Bootstrapper().WrittenBy("2.0.0")

	standing, err := gate.Standing(context.Background(), providerkit.ClassProduction)
	if err != nil {
		t.Fatalf("Standing() error = %v", err)
	}
	if !standing.Downgrade("1.0.0") {
		t.Error("Downgrade() = false where a newer build wrote the bootstrap this one is about to write")
	}
	if standing.Downgrade("3.0.0") {
		t.Error("Downgrade() = true where the build about to write is the newer one")
	}
}

func TestOccupancyRefusesWhileAnythingStandsOnTheBootstrap(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	gate, provider := gated(t, "2.0.0")

	occupancy, err := gate.Occupancy(ctx, providerkit.ClassPreview)
	if err != nil {
		t.Fatalf("Occupancy() error = %v", err)
	}
	if err := occupancy.Refuse(providerkit.ClassPreview); err != nil {
		t.Fatalf("Refuse() over an empty account = %v, want nothing in the way", err)
	}

	for _, slug := range []string{"shop", "blog"} {
		if _, err := provider.Records().Write(ctx, providerkit.Record{Name: providerkit.ProjectRecord(providerkit.ClassPreview, slug)}); err != nil {
			t.Fatal(err)
		}
	}
	wildcard, err := json.Marshal(providerkit.Wildcard{BaseDomain: "previews.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Records().Write(ctx, providerkit.Record{
		Name:  providerkit.WildcardRecord(providerkit.ClassPreview),
		Bytes: wildcard,
	}); err != nil {
		t.Fatal(err)
	}

	occupancy, err = gate.Occupancy(ctx, providerkit.ClassPreview)
	if err != nil {
		t.Fatalf("Occupancy() error = %v", err)
	}
	if !slices.Equal(occupancy.Projects, []string{"blog", "shop"}) {
		t.Errorf("Occupancy().Projects = %v, want both projects, sorted", occupancy.Projects)
	}
	var refusal providerkit.Refusal
	err = occupancy.Refuse(providerkit.ClassPreview)
	if !errors.As(err, &refusal) || refusal.Code != providerkit.CodeNotReady {
		t.Fatalf("Refuse() = %v, want a %s refusal", err, providerkit.CodeNotReady)
	}
	for _, want := range []string{
		"2 project(s) are still deployed into it: blog, shop",
		"`ocel destroy preview`",
		"*.previews.example.com",
		"`ocel bootstrap destroy preview`",
	} {
		if !strings.Contains(refusal.Message, want) {
			t.Errorf("Refuse() = %q, want it to contain %q", refusal.Message, want)
		}
	}
}

func TestASchemaNewerThanThisBuildIsRefusedWithNoEscapeHatch(t *testing.T) {
	t.Parallel()

	err := providerkit.RefuseSchemaAhead(providerkit.BootstrapSchema+1, true, providerkit.ClassProduction)
	var refusal providerkit.Refusal
	if !errors.As(err, &refusal) || refusal.Code != providerkit.CodeNotReady {
		t.Fatalf("RefuseSchemaAhead() = %v, want a %s refusal", err, providerkit.CodeNotReady)
	}
	if !strings.Contains(refusal.Message, "ocel bootstrap destroy") {
		t.Errorf("RefuseSchemaAhead() = %q, want it to name what drops the bootstrap", refusal.Message)
	}
	if strings.Contains(refusal.Message, "--force") {
		t.Errorf("RefuseSchemaAhead() = %q, want no escape hatch offered", refusal.Message)
	}
	if got := strings.Count(refusal.Message, "\n"); got != 1 {
		t.Errorf("RefuseSchemaAhead() = %q, want exactly two lines", refusal.Message)
	}
	if err := providerkit.RefuseSchemaAhead(providerkit.BootstrapSchema, true, providerkit.ClassProduction); err != nil {
		t.Errorf("RefuseSchemaAhead() at the schema this build writes = %v, want it admitted", err)
	}
}

func TestAPreviewBootstrapIsRemediatedWithItsOwnCommand(t *testing.T) {
	t.Parallel()

	gate, provider := gated(t, "2.0.0")
	bootstrapped(t, provider, providerkit.ClassPreview)

	_, err := gate.Admit(context.Background(), providerkit.ClassPreview, []string{fake.FeatureImages}, &recorder{})
	var refusal providerkit.Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("Admit() = %v, want a refusal", err)
	}
	if !strings.Contains(refusal.Message, "`ocel bootstrap preview --features ") {
		t.Errorf("Admit() = %q, want the preview bootstrap's own command", refusal.Message)
	}
}
