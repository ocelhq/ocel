package providerserver_test

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/progress"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/provider/providerserver"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

type recorder struct {
	mu      sync.Mutex
	said    []string
	warned  []string
	details []string
}

func (r *recorder) Say(message string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.said = append(r.said, message)
}

func (r *recorder) Warn(message string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.warned = append(r.warned, message)
}

func (r *recorder) Error(message string) { r.Say(message) }

func (r *recorder) Detail(message string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.details = append(r.details, message)
}

func (r *recorder) Debug(line string) { r.Detail(line) }

func (r *recorder) Span(string, time.Time, time.Time, error, ...progress.Attr) {}

func (r *recorder) sayings() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.said, "\n")
}

func (r *recorder) warnings() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.warned, "\n")
}

func newReadiness(t *testing.T, writer provider.WrittenBy) (providerserver.Readiness, *fake.Provider) {
	t.Helper()

	p := fake.NewProvider(fake.Options{Region: "nowhere"})
	return providerserver.Readiness{
		Bootstrap: p.FakeBootstrap(),
		KeyValues: p.KeyValues(),
		WrittenBy: writer,
	}, p
}

func bootstrapped(t *testing.T, p *fake.Provider, tier environment.Tier, features ...string) {
	t.Helper()

	if err := p.FakeBootstrap().Apply(context.Background(), provider.BootstrapRequest{
		Tier:     tier,
		Features: features,
	}, nil); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
}

func TestStateReadsWhatTheVendorDescribes(t *testing.T) {
	t.Parallel()

	readiness, p := newReadiness(t, "2.0.0")
	bootstrapped(t, p, environment.TierProduction, fake.FeatureCache)

	status, err := readiness.Status(context.Background(), environment.TierProduction)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if !status.Present {
		t.Fatal("Status() reports no bootstrap where one was applied")
	}
	if want := []string{fake.FeatureCache}; !slices.Equal(status.Features, want) {
		t.Errorf("Status().Features = %v, want %v", status.Features, want)
	}
	if status.WrittenBy != "1.0.0" {
		t.Errorf("Status().WrittenBy = %q, want the writer the core stack records", status.WrittenBy)
	}
	if status.RepairOnDeploy {
		t.Error("Status().RepairOnDeploy is on with no bootstrap record written")
	}
}

func TestStateReadsRepairOnDeployFromTheRecord(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	readiness, vendor := newReadiness(t, "2.0.0")
	bootstrapped(t, vendor, environment.TierProduction, fake.FeatureCache)

	if err := readiness.RecordBootstrap(ctx, environment.TierProduction, stackrecords.BootstrapSettings{RepairOnDeploy: true}); err != nil {
		t.Fatalf("RecordBootstrap() error = %v", err)
	}
	status, err := readiness.Status(ctx, environment.TierProduction)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if !status.RepairOnDeploy {
		t.Error("Status().RepairOnDeploy is off after the record said it is on")
	}

	recorded, err := vendor.KeyValues().Read(ctx, stackrecords.BootstrapKey(environment.TierProduction))
	if err != nil {
		t.Fatalf("Read() of the bootstrap record = %v", err)
	}
	var settings stackrecords.BootstrapSettings
	if err := json.Unmarshal(recorded.Value, &settings); err != nil || !settings.RepairOnDeploy {
		t.Fatalf("the bootstrap record contains %q, %v, want repair_on_deploy on", recorded.Value, err)
	}
}

type storeTheBootstrapCreates struct {
	keyvalue.Store
	bootstrap provider.Bootstrap
}

func (s storeTheBootstrapCreates) refuseBeforeBootstrap(ctx context.Context, tier environment.Tier) error {
	described, err := s.bootstrap.Describe(ctx, tier)
	if err != nil {
		return err
	}
	if !described.Present {
		return refusal.Refuse(refusal.CodeNotReady, "this project keeps no store yet")
	}
	return nil
}

func (s storeTheBootstrapCreates) Read(ctx context.Context, key keyvalue.Key) (keyvalue.Entry, error) {
	if err := s.refuseBeforeBootstrap(ctx, key.Partition.Tier); err != nil {
		return keyvalue.Entry{}, err
	}
	return s.Store.Read(ctx, key)
}

func (s storeTheBootstrapCreates) Write(ctx context.Context, entry keyvalue.Entry) (keyvalue.Revision, error) {
	if err := s.refuseBeforeBootstrap(ctx, entry.Key.Partition.Tier); err != nil {
		return "", err
	}
	return s.Store.Write(ctx, entry)
}

func (s storeTheBootstrapCreates) List(ctx context.Context, in keyvalue.Partition, under ...string) ([]keyvalue.Entry, error) {
	if err := s.refuseBeforeBootstrap(ctx, in.Tier); err != nil {
		return nil, err
	}
	return s.Store.List(ctx, in, under...)
}

type vendorWithAStoreTheBootstrapCreates struct {
	*fake.Provider
}

func (p vendorWithAStoreTheBootstrapCreates) KeyValues() keyvalue.Store {
	return storeTheBootstrapCreates{Store: p.Provider.KeyValues(), bootstrap: p.FakeBootstrap()}
}

func TestStateReportsNoBootstrapWhereTheStoreItWouldRecordInIsNotThere(t *testing.T) {
	t.Parallel()

	readiness, p := newReadiness(t, "2.0.0")
	readiness.KeyValues = vendorWithAStoreTheBootstrapCreates{p}.KeyValues()

	status, err := readiness.Status(context.Background(), environment.TierProduction)
	if err != nil {
		t.Fatalf("Status() error = %v, want no bootstrap reported", err)
	}
	if status.Present {
		t.Error("Status() reports a bootstrap where none was applied")
	}
}

type missingStore struct{ keyvalue.Store }

func (missingStore) Read(context.Context, keyvalue.Key) (keyvalue.Entry, error) {
	return keyvalue.Entry{}, refusal.Refuse(refusal.CodeNotReady, "this project keeps no store yet")
}

func (missingStore) List(context.Context, keyvalue.Partition, ...string) ([]keyvalue.Entry, error) {
	return nil, refusal.Refuse(refusal.CodeNotReady, "this project keeps no store yet")
}

func TestStateReadsABootstrapInterruptedBeforeItCreatedItsStoreAsUnfinished(t *testing.T) {
	t.Parallel()

	readiness, p := newReadiness(t, "2.0.0")
	bootstrapped(t, p, environment.TierProduction)
	p.FakeBootstrap().MarkUnfinished()
	readiness.KeyValues = missingStore{p.KeyValues()}

	status, err := readiness.Status(context.Background(), environment.TierProduction)
	if err != nil {
		t.Fatalf("Status() error = %v, want the unfinished bootstrap reported so a bootstrap can finish it", err)
	}
	if !status.Unfinished {
		t.Error("Status() reads a bootstrap that never created its store as finished")
	}
}

func TestNothingDependsOnABootstrapThatNeverCreatedItsStore(t *testing.T) {
	t.Parallel()

	readiness, p := newReadiness(t, "2.0.0")
	bootstrapped(t, p, environment.TierProduction)
	readiness.KeyValues = missingStore{p.KeyValues()}

	if err := readiness.RefuseIfInUse(context.Background(), environment.TierProduction); err != nil {
		t.Errorf("RefuseIfInUse() = %v, want nothing in use where no store was ever created", err)
	}
}

func TestEnsureReadyRefusesABootstrapThatIsNotThere(t *testing.T) {
	t.Parallel()

	readiness, _ := newReadiness(t, "2.0.0")

	_, err := readiness.EnsureReady(context.Background(), environment.TierPreview, "", nil, true, &recorder{})
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeNotReady {
		t.Fatalf("EnsureReady() = %v, want a %s refusal", err, refusal.CodeNotReady)
	}
	if !strings.Contains(refused.Message, "`ocel bootstrap preview`") {
		t.Errorf("EnsureReady() = %q, want it to name the command that creates the preview bootstrap", refused.Message)
	}
}

func TestEnsureReadyRefusesAMissingPartAndNamesTheBootstrapCommandAlone(t *testing.T) {
	t.Parallel()

	readiness, vendor := newReadiness(t, "2.0.0")
	bootstrapped(t, vendor, environment.TierProduction, fake.FeatureCache)

	_, err := readiness.EnsureReady(context.Background(), environment.TierProduction, "shop", []string{fake.FeatureImages}, true, &recorder{})
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeNotReady {
		t.Fatalf("EnsureReady() = %v, want a %s refusal", err, refusal.CodeNotReady)
	}
	for _, want := range []string{
		"lacks a part this project wants: " + fake.FeatureImages,
		"Run `ocel bootstrap production`",
	} {
		if !strings.Contains(refused.Message, want) {
			t.Errorf("EnsureReady() = %q, want it to contain %q", refused.Message, want)
		}
	}
	if strings.Contains(refused.Message, "--features") {
		t.Errorf("EnsureReady() = %q, want no --features in the command it names", refused.Message)
	}
}

func TestEnsureReadyRecordsThatTheProjectWantsThePartsTheBootstrapLacks(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	readiness, vendor := newReadiness(t, "2.0.0")
	bootstrapped(t, vendor, environment.TierProduction, fake.FeatureCache)

	if _, err := readiness.EnsureReady(ctx, environment.TierProduction, "shop", []string{fake.FeatureCache, fake.FeatureImages}, true, &recorder{}); err == nil {
		t.Fatal("EnsureReady() = nil, want a refusal: images is missing")
	}

	wants, err := stackrecords.ListBootstrapWants(ctx, vendor.KeyValues(), environment.TierProduction)
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string][]string{fake.FeatureImages: {"shop"}}; !maps.EqualFunc(wants, want, slices.Equal) {
		t.Errorf("ListBootstrapWants() = %v, want %v", wants, want)
	}
}

func TestEnsureReadyNamingNoProjectRecordsNoWant(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	readiness, vendor := newReadiness(t, "2.0.0")
	bootstrapped(t, vendor, environment.TierProduction, fake.FeatureCache)

	_, err := readiness.EnsureReady(ctx, environment.TierProduction, "", []string{fake.FeatureImages}, false, &recorder{})
	if err == nil {
		t.Fatal("EnsureReady() = nil, want a refusal: images is missing")
	}
	if want := "deploy without --dry, which records what this project wants, then run `ocel bootstrap production`"; !strings.Contains(err.Error(), want) {
		t.Errorf("EnsureReady() error = %v, want it to say %q: nothing was recorded for the bootstrap to add", err, want)
	}

	wants, err := stackrecords.ListBootstrapWants(ctx, vendor.KeyValues(), environment.TierProduction)
	if err != nil || len(wants) != 0 {
		t.Errorf("ListBootstrapWants() = %v, %v, want nothing recorded", wants, err)
	}
}

func TestEnsureReadyRepairsAStaleBootstrapWithoutAcceptingReplacements(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	readiness, vendor := newReadiness(t, "2.0.0")
	bootstrap := vendor.FakeBootstrap()
	bootstrapped(t, vendor, environment.TierProduction, fake.FeatureCache)
	if err := readiness.RecordBootstrap(ctx, environment.TierProduction, stackrecords.BootstrapSettings{RepairOnDeploy: true}); err != nil {
		t.Fatal(err)
	}
	bootstrap.MarkStale(fake.FeatureCache)

	progress := &recorder{}
	status, err := readiness.EnsureReady(ctx, environment.TierProduction, "", []string{fake.FeatureCache}, true, progress)
	if err != nil {
		t.Fatalf("EnsureReady() error = %v", err)
	}
	if stale := status.Stale([]string{fake.FeatureCache}); len(stale) != 0 {
		t.Errorf("EnsureReady() left %v behind, want the repair to have refreshed them", stale)
	}

	applied := bootstrap.Applied()
	repairing := applied[len(applied)-1]
	if !repairing.RefuseReplacements {
		t.Error("the repair reached Apply() allowed to replace resources, and nothing is there to accept a replacement")
	}
	if !slices.Equal(repairing.Features, []string{fake.FeatureCache}) {
		t.Errorf("the repair applied %v, want the features already installed", repairing.Features)
	}
}

func TestEnsureReadyLeavesAStaleBootstrapAloneWhenTheAccountNeverOptedIntoRepair(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	readiness, vendor := newReadiness(t, "2.0.0")
	bootstrap := vendor.FakeBootstrap()
	bootstrapped(t, vendor, environment.TierProduction, fake.FeatureCache)
	bootstrap.MarkStale(fake.FeatureCache)

	progress := &recorder{}
	if _, err := readiness.EnsureReady(ctx, environment.TierProduction, "", []string{fake.FeatureCache}, true, progress); err != nil {
		t.Fatalf("EnsureReady() error = %v", err)
	}
	if got := len(bootstrap.Applied()); got != 1 {
		t.Errorf("Apply() ran %d times, want only the bootstrap that installed it", got)
	}
	if !strings.Contains(progress.warnings(), "its content is behind") {
		t.Errorf("EnsureReady() warned %q, want a warning about the drift it left state", progress.warnings())
	}
}

func TestEnsureReadyAsksForNoRepairAndGetsNone(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	readiness, vendor := newReadiness(t, "2.0.0")
	bootstrap := vendor.FakeBootstrap()
	bootstrapped(t, vendor, environment.TierProduction, fake.FeatureCache)
	if err := readiness.RecordBootstrap(ctx, environment.TierProduction, stackrecords.BootstrapSettings{RepairOnDeploy: true}); err != nil {
		t.Fatal(err)
	}
	bootstrap.MarkStale(fake.FeatureCache)

	progress := &recorder{}
	status, err := readiness.EnsureReady(ctx, environment.TierProduction, "", []string{fake.FeatureCache}, false, progress)
	if err != nil {
		t.Fatalf("EnsureReady() error = %v", err)
	}
	if got := len(bootstrap.Applied()); got != 1 {
		t.Errorf("Apply() ran %d times, want a caller that asked for no repairing to get none though the account opted in", got)
	}
	if stale := status.Stale([]string{fake.FeatureCache}); len(stale) != 1 {
		t.Errorf("EnsureReady() reports %v behind, want the drift it was told to leave state", stale)
	}
	if !strings.Contains(progress.warnings(), "its content is behind") {
		t.Errorf("EnsureReady() warned %q, want a warning about the drift it left state", progress.warnings())
	}
}

func TestEnsureReadyNeverRepairsOrCallsStaleABootstrapPartItCouldNotRead(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	readiness, vendor := newReadiness(t, "2.0.0")
	bootstrap := vendor.FakeBootstrap()
	bootstrapped(t, vendor, environment.TierProduction, fake.FeatureCache)
	if err := readiness.RecordBootstrap(ctx, environment.TierProduction, stackrecords.BootstrapSettings{RepairOnDeploy: true}); err != nil {
		t.Fatal(err)
	}
	bootstrap.MarkUnreadable(fake.FeatureCache, "the edge was rate limited")

	progress := &recorder{}
	status, err := readiness.EnsureReady(ctx, environment.TierProduction, "", []string{fake.FeatureCache}, true, progress)
	if err != nil {
		t.Fatalf("EnsureReady() error = %v", err)
	}
	if got := len(bootstrap.Applied()); got != 1 {
		t.Errorf("Apply() ran %d times, want no repair of a part nobody could read", got)
	}
	if stale := status.Stale([]string{fake.FeatureCache}); len(stale) != 0 {
		t.Errorf("EnsureReady() reports %v behind, want nothing called stale that could not be read", stale)
	}
	warned := progress.warnings()
	if strings.Contains(warned, "its content is behind") {
		t.Errorf("EnsureReady() warned %q, want no claim the bootstrap is behind", warned)
	}
	if !strings.Contains(warned, "the edge was rate limited") {
		t.Errorf("EnsureReady() warned %q, want it to say why the part could not be read", warned)
	}
}

func TestEnsureReadyWillNotRepairFromADevelopmentBuild(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	readiness, vendor := newReadiness(t, "dev+cafebabe")
	bootstrap := vendor.FakeBootstrap()
	bootstrapped(t, vendor, environment.TierProduction, fake.FeatureCache)
	if err := readiness.RecordBootstrap(ctx, environment.TierProduction, stackrecords.BootstrapSettings{RepairOnDeploy: true}); err != nil {
		t.Fatal(err)
	}
	bootstrap.MarkStale(fake.FeatureCache)

	progress := &recorder{}
	if _, err := readiness.EnsureReady(ctx, environment.TierProduction, "", []string{fake.FeatureCache}, true, progress); err != nil {
		t.Fatalf("EnsureReady() error = %v", err)
	}
	if got := len(bootstrap.Applied()); got != 1 {
		t.Errorf("Apply() ran %d times, want a development build to leave the account unchanged", got)
	}
	if !strings.Contains(progress.sayings(), "development build (dev+cafebabe)") {
		t.Errorf("EnsureReady() said %q, want it to say which build declined to repair", progress.sayings())
	}
}

func TestEnsureReadyReportsARepairTheCredentialsCannotDo(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	readiness, vendor := newReadiness(t, "2.0.0")
	bootstrap := vendor.FakeBootstrap()
	bootstrapped(t, vendor, environment.TierProduction, fake.FeatureCache)
	if err := readiness.RecordBootstrap(ctx, environment.TierProduction, stackrecords.BootstrapSettings{RepairOnDeploy: true}); err != nil {
		t.Fatal(err)
	}
	bootstrap.MarkStale(fake.FeatureCache)
	bootstrap.RefuseApply(refusal.Refuse(refusal.CodeDenied,
		"ocel-deploy@10.0.0.4 can neither act as root nor run sudo without a password"))

	progress := &recorder{}
	if _, err := readiness.EnsureReady(ctx, environment.TierProduction, "", []string{fake.FeatureCache}, true, progress); err != nil {
		t.Fatalf("EnsureReady() error = %v, want a refused repair to leave the run state", err)
	}
	if !strings.Contains(progress.warnings(), "ocel-deploy@10.0.0.4 can neither act as root nor run sudo without a password") {
		t.Errorf("EnsureReady() warned %q, want a warning with the provider's own account of why the repair was denied", progress.warnings())
	}
	written, _, _ := strings.Cut(refusedLine(t, progress), ": ")
	for _, vendored := range []string{"account", "stack"} {
		if strings.Contains(written, vendored) {
			t.Errorf("providerserver wrote %q, and it speaks %q at a host that has no such thing", written, vendored)
		}
	}
}

func refusedLine(t *testing.T, progress *recorder) string {
	t.Helper()

	for _, line := range strings.Split(progress.warnings(), "\n") {
		if strings.HasPrefix(line, "This run may not refresh") {
			return line
		}
	}
	t.Fatalf("no warning in %q says the repair was denied", progress.warnings())
	return ""
}

func TestADeniedRepairWithNothingToSayStillReadsAsASentence(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	readiness, vendor := newReadiness(t, "2.0.0")
	bootstrap := vendor.FakeBootstrap()
	bootstrapped(t, vendor, environment.TierProduction, fake.FeatureCache)
	if err := readiness.RecordBootstrap(ctx, environment.TierProduction, stackrecords.BootstrapSettings{RepairOnDeploy: true}); err != nil {
		t.Fatal(err)
	}
	bootstrap.MarkStale(fake.FeatureCache)
	bootstrap.RefuseApply(refusal.Refuse(refusal.CodeDenied, ""))

	progress := &recorder{}
	if _, err := readiness.EnsureReady(ctx, environment.TierProduction, "", []string{fake.FeatureCache}, true, progress); err != nil {
		t.Fatalf("EnsureReady() error = %v, want a refused repair to leave the run state", err)
	}
	if line := refusedLine(t, progress); strings.Contains(line, ": ") {
		t.Errorf("providerserver wrote %q, want no colon introducing a reason the provider never gave", line)
	}
}

func TestEnsureReadyWarnsThatARepairFailedAndCarriesOnWithTheBootstrapInPlace(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	readiness, vendor := newReadiness(t, "2.0.0")
	bootstrap := vendor.FakeBootstrap()
	bootstrapped(t, vendor, environment.TierProduction, fake.FeatureCache)
	if err := readiness.RecordBootstrap(ctx, environment.TierProduction, stackrecords.BootstrapSettings{RepairOnDeploy: true}); err != nil {
		t.Fatal(err)
	}
	bootstrap.MarkStale(fake.FeatureCache)
	bootstrap.RefuseApply(errors.New("the stack update timed out"))

	progress := &recorder{}
	if _, err := readiness.EnsureReady(ctx, environment.TierProduction, "", []string{fake.FeatureCache}, true, progress); err != nil {
		t.Fatalf("EnsureReady() error = %v, want a failed repair to leave the run standing", err)
	}
	want := "Could not refresh the production bootstrap, so this run continues against the one in place: the stack update timed out"
	if !strings.Contains(progress.warnings(), want) {
		t.Errorf("EnsureReady() warned %q, want %q", progress.warnings(), want)
	}
}

func TestAnApplyThatNeverFinishedReachesTheCLIAsOneAndReadsAsDrifted(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	readiness, vendor := newReadiness(t, "2.0.0")
	tier := environment.TierProduction
	bootstrapped(t, vendor, tier, fake.FeatureCache)
	vendor.FakeBootstrap().MarkUnfinished()

	status, err := readiness.Status(ctx, tier)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Unfinished {
		t.Fatal("Status() reads a half-applied bootstrap as one that finished, so nothing downstream can banner it")
	}
	handed := providerserver.BootstrapStatusProto(status, "2.0.0", environmentv1.Tier_TIER_PRODUCTION, status.Features)
	if !handed.GetUnfinished() {
		t.Error("the status the CLI is handed says nothing about the apply that never finished")
	}
}

func TestDowngradeIsAWriterOlderThanTheOneThatWrote(t *testing.T) {
	t.Parallel()

	readiness, vendor := newReadiness(t, "1.0.0")
	bootstrapped(t, vendor, environment.TierProduction)
	vendor.FakeBootstrap().SetWriter("2.0.0")

	status, err := readiness.Status(context.Background(), environment.TierProduction)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if !status.Downgrade("1.0.0") {
		t.Error("Downgrade() = false where a newer build wrote the bootstrap this one is about to write")
	}
	if status.Downgrade("3.0.0") {
		t.Error("Downgrade() = true where the build about to write is the newer one")
	}
}

func TestBootstrapUsersRefuseWhileAnythingDependsOnTheBootstrap(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	readiness, vendor := newReadiness(t, "2.0.0")

	users, err := readiness.BootstrapUsers(ctx, environment.TierPreview)
	if err != nil {
		t.Fatalf("BootstrapUsers() error = %v", err)
	}
	if err := users.Refuse(environment.TierPreview); err != nil {
		t.Fatalf("Refuse() over an empty account = %v, want nothing in the way", err)
	}

	for _, slug := range []string{"shop", "blog"} {
		if _, err := vendor.KeyValues().Write(ctx, keyvalue.Entry{Key: stackrecords.ProjectKey(environment.TierPreview, slug), Value: json.RawMessage("{}")}); err != nil {
			t.Fatal(err)
		}
	}
	wildcard, err := json.Marshal(stackrecords.Wildcard{BaseDomain: "previews.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := vendor.KeyValues().Write(ctx, keyvalue.Entry{
		Key:   stackrecords.WildcardKey(environment.TierPreview),
		Value: wildcard,
	}); err != nil {
		t.Fatal(err)
	}

	users, err = readiness.BootstrapUsers(ctx, environment.TierPreview)
	if err != nil {
		t.Fatalf("BootstrapUsers() error = %v", err)
	}
	if !slices.Equal(users.Projects, []string{"blog", "shop"}) {
		t.Errorf("BootstrapUsers().Projects = %v, want both projects, sorted", users.Projects)
	}
	var refused refusal.Refusal
	err = users.Refuse(environment.TierPreview)
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

func TestAPreviewBootstrapIsRemediatedWithItsOwnCommand(t *testing.T) {
	t.Parallel()

	readiness, vendor := newReadiness(t, "2.0.0")
	bootstrapped(t, vendor, environment.TierPreview)

	_, err := readiness.EnsureReady(context.Background(), environment.TierPreview, "", []string{fake.FeatureImages}, true, &recorder{})
	var refused refusal.Refusal
	if !errors.As(err, &refused) {
		t.Fatalf("EnsureReady() = %v, want a refusal", err)
	}
	if !strings.Contains(refused.Message, "`ocel bootstrap preview`") {
		t.Errorf("EnsureReady() = %q, want the preview bootstrap's own command", refused.Message)
	}
}
