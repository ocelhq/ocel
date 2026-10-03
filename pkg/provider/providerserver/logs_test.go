package providerserver_test

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	connect "connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/provider/ledger"
	"github.com/ocelhq/ocel/pkg/provider/resources"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

var logsEpoch = time.Date(2026, time.January, 5, 12, 0, 0, 0, time.UTC)

type recordedApp struct {
	app        string
	seq        int
	functions  []provider.Function
	containers []provider.AppContainer
}

func recordApps(t *testing.T, vendor *fake.Provider, tier environment.Tier, env string, apps ...recordedApp) {
	t.Helper()
	for _, recorded := range apps {
		build, err := provider.ParseBuild(buildIdentity(recorded.seq))
		if err != nil {
			t.Fatal(err)
		}
		err = stackrecords.Write(context.Background(), vendor.KeyValues(), tier, "shop", naming.AppStack(env, recorded.app, build.Release()), stackrecords.Stack{
			Kind:       provider.StackApp,
			App:        recorded.app,
			Release:    build.Release().String(),
			Build:      build.String(),
			Functions:  recorded.functions,
			Containers: recorded.containers,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func webFunction(seq int) []provider.Function {
	return []provider.Function{{Name: "web-server", Physical: "web-fn-" + string(rune('a'+seq))}}
}

func promoteBuilds(t *testing.T, vendor *fake.Provider, tier environment.Tier, pointer string, builds map[string]string) {
	t.Helper()
	promotion := router.Promotion{PromotionID: "p-" + pointer, Ts: 1, Builds: builds}
	if _, err := ledger.New(vendor.KeyValues(), tier, "shop").Promote(context.Background(), promotion, pointer, ""); err != nil {
		t.Fatal(err)
	}
}

func logLine(offset time.Duration, message string) provider.LogEntry {
	return provider.LogEntry{Time: logsEpoch.Add(offset), Message: message, Instance: "i-1"}
}

func logsRequest() *contractv1.ReadLogsRequest {
	return &contractv1.ReadLogsRequest{
		Slug:  "shop",
		Since: timestamppb.New(logsEpoch.Add(-time.Hour)),
		Limit: 100,
	}
}

type readLogs struct {
	entries []*contractv1.LogEntry
	notices []*contractv1.LogNotice
	batches int
	order   []string
}

func (r readLogs) messages() []string {
	var messages []string
	for _, entry := range r.entries {
		messages = append(messages, entry.GetMessage())
	}
	return messages
}

func readLogsOf(t *testing.T, client contractv1connect.ProviderServiceClient, req *contractv1.ReadLogsRequest) (readLogs, error) {
	t.Helper()
	stream, err := client.ReadLogs(context.Background(), req)
	if err != nil {
		return readLogs{}, err
	}
	defer stream.Close()
	var read readLogs
	for stream.Receive() {
		switch body := stream.Msg().GetBody().(type) {
		case *contractv1.ReadLogsResponse_Batch:
			read.batches++
			read.order = append(read.order, "batch")
			read.entries = append(read.entries, body.Batch.GetEntries()...)
		case *contractv1.ReadLogsResponse_Notice:
			read.order = append(read.order, body.Notice.GetKind().String())
			read.notices = append(read.notices, body.Notice)
		}
	}
	return read, stream.Err()
}

func twoReleasesOfWeb(t *testing.T) (contractv1connect.ProviderServiceClient, *fake.Provider) {
	t.Helper()
	client, vendor := contractServed(t, "1.0.0")
	edgeProvisioned(t, vendor, environment.TierProduction, "shop")
	recordApps(t, vendor, environment.TierProduction, stackrecords.ProductionEnv,
		recordedApp{app: "web", seq: 0, functions: webFunction(0)},
		recordedApp{app: "web", seq: 1, functions: webFunction(1)},
		recordedApp{app: "api", seq: 2, functions: []provider.Function{{Name: "api-server", Physical: "api-fn"}}},
	)
	promoteBuilds(t, vendor, environment.TierProduction, router.DefaultPointer, map[string]string{"web": buildIdentity(1), "api": buildIdentity(2)})
	vendor.FakeLogs().Append("web-fn-a", logLine(1*time.Minute, "old web"))
	vendor.FakeLogs().Append("web-fn-b", logLine(2*time.Minute, "live web"))
	vendor.FakeLogs().Append("api-fn", logLine(3*time.Minute, "live api"))
	return client, vendor
}

func TestReadLogsReadsOnlyTheActiveReleaseOfEachApp(t *testing.T) {
	t.Parallel()
	client, _ := twoReleasesOfWeb(t)

	read, err := readLogsOf(t, client, logsRequest())
	if err != nil {
		t.Fatalf("ReadLogs() error = %v", err)
	}
	if got, want := read.messages(), []string{"live web", "live api"}; !slices.Equal(got, want) {
		t.Errorf("ReadLogs() read %v, want %v: the release the pointer no longer names is not the app's live one", got, want)
	}
}

func TestReadLogsReadsEveryReleaseWhenAsked(t *testing.T) {
	t.Parallel()
	client, _ := twoReleasesOfWeb(t)

	req := logsRequest()
	req.AllReleases = true
	read, err := readLogsOf(t, client, req)
	if err != nil {
		t.Fatalf("ReadLogs() error = %v", err)
	}
	if got, want := read.messages(), []string{"old web", "live web", "live api"}; !slices.Equal(got, want) {
		t.Errorf("ReadLogs() with all releases read %v, want %v", got, want)
	}
	releases := map[string]bool{}
	for _, entry := range read.entries {
		if entry.GetApp() == "web" {
			releases[entry.GetRelease()] = true
		}
	}
	if len(releases) != 2 {
		t.Errorf("ReadLogs() labelled web's entries with releases %v, want the two releases apart", releases)
	}
}

func TestReadLogsReadsOnlyTheAppsNamed(t *testing.T) {
	t.Parallel()
	client, _ := twoReleasesOfWeb(t)

	req := logsRequest()
	req.Apps = []string{"api"}
	read, err := readLogsOf(t, client, req)
	if err != nil {
		t.Fatalf("ReadLogs() error = %v", err)
	}
	if got, want := read.messages(), []string{"live api"}; !slices.Equal(got, want) {
		t.Errorf("ReadLogs() of api read %v, want %v", got, want)
	}
	if read.entries[0].GetApp() != "api" || read.entries[0].GetSource() != "http" {
		t.Errorf("ReadLogs() labelled the entry %q from %q, want api from http", read.entries[0].GetApp(), read.entries[0].GetSource())
	}
}

func TestReadLogsRefusesAnAppThatIsNotDeployed(t *testing.T) {
	t.Parallel()
	client, _ := twoReleasesOfWeb(t)

	req := logsRequest()
	req.Apps = []string{"admin"}
	_, err := readLogsOf(t, client, req)
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("ReadLogs() error = %v, want an invalid-argument refusal", err)
	}
	for _, want := range []string{"admin", "api", "web"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("ReadLogs() refused with %q, want it to name %q", err, want)
		}
	}
}

func TestReadLogsEndsWithCaughtUp(t *testing.T) {
	t.Parallel()
	client, _ := twoReleasesOfWeb(t)

	read, err := readLogsOf(t, client, logsRequest())
	if err != nil {
		t.Fatalf("ReadLogs() error = %v", err)
	}
	if want := []string{"batch", contractv1.LogNotice_KIND_CAUGHT_UP.String()}; !slices.Equal(read.order, want) {
		t.Errorf("ReadLogs() sent %v, want %v", read.order, want)
	}
}

func TestReadLogsSendsOnlyCaughtUpWhenNothingIsDeployed(t *testing.T) {
	t.Parallel()
	client, _ := contractServed(t, "1.0.0")

	read, err := readLogsOf(t, client, logsRequest())
	if err != nil {
		t.Fatalf("ReadLogs() error = %v", err)
	}
	if want := []string{contractv1.LogNotice_KIND_CAUGHT_UP.String()}; !slices.Equal(read.order, want) {
		t.Errorf("ReadLogs() of a project with no deploys sent %v, want %v", read.order, want)
	}
}

func TestReadLogsLabelsAWorkersEntriesWithTheWorker(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.0.0")
	edgeProvisioned(t, vendor, environment.TierProduction, "shop")
	recordApps(t, vendor, environment.TierProduction, stackrecords.ProductionEnv, recordedApp{
		app: "web", seq: 0,
		functions: []provider.Function{
			{Name: "web-server", Physical: "web-fn"},
			{Name: resources.WorkerName("media"), Physical: "media-fn"},
		},
		containers: []provider.AppContainer{{Name: resources.WorkerName("mailer"), Physical: "mailer-box"}},
	})
	promoteBuilds(t, vendor, environment.TierProduction, router.DefaultPointer, map[string]string{"web": buildIdentity(0)})
	vendor.FakeLogs().Append("web-fn", logLine(1*time.Minute, "served"))
	vendor.FakeLogs().Append("media-fn", logLine(2*time.Minute, "resized"))
	vendor.FakeLogs().Append("mailer-box", logLine(3*time.Minute, "sent"))

	read, err := readLogsOf(t, client, logsRequest())
	if err != nil {
		t.Fatalf("ReadLogs() error = %v", err)
	}
	sources := map[string]string{}
	for _, entry := range read.entries {
		sources[entry.GetMessage()] = entry.GetSource()
	}
	want := map[string]string{"served": "http", "resized": "media", "sent": "mailer"}
	if len(sources) != len(want) {
		t.Fatalf("ReadLogs() read %v, want one entry of each of %v", sources, want)
	}
	for message, source := range want {
		if sources[message] != source {
			t.Errorf("ReadLogs() labelled %q with source %q, want %q", message, sources[message], source)
		}
	}
}

func TestReadLogsReadsThePreviewEnvironmentItIsAskedFor(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.0.0")
	edgeProvisioned(t, vendor, environment.TierPreview, "shop")
	recordApps(t, vendor, environment.TierPreview, "feature-x", recordedApp{app: "web", seq: 0, functions: []provider.Function{{Name: "web-server", Physical: "x-fn"}}})
	recordApps(t, vendor, environment.TierPreview, "feature-y", recordedApp{app: "web", seq: 1, functions: []provider.Function{{Name: "web-server", Physical: "y-fn"}}})
	promoteBuilds(t, vendor, environment.TierPreview, "feature-x", map[string]string{"web": buildIdentity(0)})
	promoteBuilds(t, vendor, environment.TierPreview, "feature-y", map[string]string{"web": buildIdentity(1)})
	vendor.FakeLogs().Append("x-fn", logLine(time.Minute, "from x"))
	vendor.FakeLogs().Append("y-fn", logLine(time.Minute, "from y"))

	req := logsRequest()
	req.Environment = &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PREVIEW, Identity: "feature-x"}
	read, err := readLogsOf(t, client, req)
	if err != nil {
		t.Fatalf("ReadLogs() error = %v", err)
	}
	if got, want := read.messages(), []string{"from x"}; !slices.Equal(got, want) {
		t.Errorf("ReadLogs() of feature-x read %v, want %v", got, want)
	}
}

func TestReadLogsReadsNothingLiveWhenTheEnvironmentWasNeverPromoted(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.0.0")
	edgeProvisioned(t, vendor, environment.TierProduction, "shop")
	recordApps(t, vendor, environment.TierProduction, stackrecords.ProductionEnv, recordedApp{app: "web", seq: 0, functions: webFunction(0)})
	vendor.FakeLogs().Append("web-fn-a", logLine(time.Minute, "unreleased"))

	read, err := readLogsOf(t, client, logsRequest())
	if err != nil {
		t.Fatalf("ReadLogs() error = %v", err)
	}
	if len(read.entries) != 0 {
		t.Errorf("ReadLogs() read %v from an app no promotion serves, want nothing", read.messages())
	}
}

func TestReadLogsPassesTheWindowTheLimitAndTheFilterToTheProvider(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.0.0")
	edgeProvisioned(t, vendor, environment.TierProduction, "shop")
	recordApps(t, vendor, environment.TierProduction, stackrecords.ProductionEnv, recordedApp{app: "web", seq: 0, functions: webFunction(0)})
	promoteBuilds(t, vendor, environment.TierProduction, router.DefaultPointer, map[string]string{"web": buildIdentity(0)})
	for i, message := range []string{"boot", "error one", "idle", "error two", "error three", "error four"} {
		vendor.FakeLogs().Append("web-fn-a", logLine(time.Duration(i)*time.Minute, message))
	}

	req := logsRequest()
	req.Since = timestamppb.New(logsEpoch.Add(time.Minute))
	req.Until = timestamppb.New(logsEpoch.Add(4 * time.Minute))
	req.Contains = "error"
	req.Limit = 2
	read, err := readLogsOf(t, client, req)
	if err != nil {
		t.Fatalf("ReadLogs() error = %v", err)
	}
	if got, want := read.messages(), []string{"error two", "error three"}; !slices.Equal(got, want) {
		t.Errorf("ReadLogs() read %v, want %v", got, want)
	}
}

func TestReadLogsRefusesAReadWithNoStartTime(t *testing.T) {
	t.Parallel()
	client, _ := twoReleasesOfWeb(t)

	req := logsRequest()
	req.Since = nil
	if _, err := readLogsOf(t, client, req); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("ReadLogs() with no since error = %v, want an invalid-argument refusal", err)
	}
}

func TestReadLogsRefusesALimitOutsideWhatAReadCanReturn(t *testing.T) {
	t.Parallel()
	client, _ := twoReleasesOfWeb(t)

	for _, limit := range []uint32{0, 10001} {
		req := logsRequest()
		req.Limit = limit
		if _, err := readLogsOf(t, client, req); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("ReadLogs() with limit %d error = %v, want an invalid-argument refusal", limit, err)
		}
	}
}

func TestReadLogsStopsWhenTheProviderRefusesTheRead(t *testing.T) {
	t.Parallel()
	vendor := fake.NewProvider(fake.Options{Region: "nowhere"}).WithProjectDir(workingDir(t))
	client := servedProvider(t, "1.0.0", failingLogs{Provider: vendor})
	edgeProvisioned(t, vendor, environment.TierProduction, "shop")
	recordApps(t, vendor, environment.TierProduction, stackrecords.ProductionEnv, recordedApp{app: "web", seq: 0, functions: webFunction(0)})
	promoteBuilds(t, vendor, environment.TierProduction, router.DefaultPointer, map[string]string{"web": buildIdentity(0)})

	read, err := readLogsOf(t, client, logsRequest())
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("ReadLogs() error = %v, want the provider's refusal passed on", err)
	}
	if len(read.order) != 0 {
		t.Errorf("ReadLogs() sent %v before the refusal, want nothing, and above all no CAUGHT_UP", read.order)
	}
}

type failingLogs struct{ *fake.Provider }

type partlyMissingLogs struct{ *fake.Provider }

func (p partlyMissingLogs) Logs() provider.Logs { return missingLogs{p.Provider.Logs()} }

type missingLogs struct{ provider.Logs }

func (m missingLogs) Read(ctx context.Context, q provider.LogQuery, emit func([]provider.LogEntry) error) error {
	if err := m.Logs.Read(ctx, q, emit); err != nil {
		return err
	}
	return provider.LogTargetsMissing{Targets: q.Targets[:1]}
}

func TestReadLogsNamesTheReleaseOfASourceThatNoLongerExists(t *testing.T) {
	t.Parallel()
	vendor := fake.NewProvider(fake.Options{Region: "nowhere"}).WithProjectDir(workingDir(t))
	client := servedProvider(t, "1.0.0", partlyMissingLogs{Provider: vendor})
	edgeProvisioned(t, vendor, environment.TierProduction, "shop")
	recordApps(t, vendor, environment.TierProduction, stackrecords.ProductionEnv, recordedApp{app: "web", seq: 0, functions: webFunction(0)})
	promoteBuilds(t, vendor, environment.TierProduction, router.DefaultPointer, map[string]string{"web": buildIdentity(0)})
	build, err := provider.ParseBuild(buildIdentity(0))
	if err != nil {
		t.Fatal(err)
	}

	req := logsRequest()
	req.AllReleases = true
	read, err := readLogsOf(t, client, req)
	if err != nil {
		t.Fatalf("ReadLogs() error = %v, want a source that is gone reported and not refused", err)
	}
	if want := []string{contractv1.LogNotice_KIND_SOURCE_GONE.String(), contractv1.LogNotice_KIND_CAUGHT_UP.String()}; !slices.Equal(read.order, want) {
		t.Fatalf("ReadLogs() sent %v, want %v", read.order, want)
	}
	if message := read.notices[0].GetMessage(); !strings.Contains(message, build.Release().String()) || !strings.Contains(message, "web") {
		t.Errorf("ReadLogs() said %q of the missing source, want it to name its app and release %s", message, build.Release())
	}
}

func (failingLogs) Logs() provider.Logs { return refusingLogs{} }

type refusingLogs struct{}

func (refusingLogs) Read(context.Context, provider.LogQuery, func([]provider.LogEntry) error) error {
	return refusal.Refuse(refusal.CodeNotReady, "the log store is not reachable")
}

func TestAStackRecordNamesTheBuildThePromotionPointsAt(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)

	result, _ := deploy(t, client, deployRequest())
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	active, err := ledger.New(vendor.KeyValues(), environment.TierProduction, "shop").Read(context.Background(), router.DefaultPointer)
	if err != nil {
		t.Fatal(err)
	}
	var promoted router.Promotion
	for _, recorded := range active.Promotions {
		if recorded.PromotionID == active.Active {
			promoted = recorded.Promotion
		}
	}
	if len(promoted.Builds) == 0 {
		t.Fatalf("the deploy's active promotion %q names no builds", active.Active)
	}
	stacks, err := stackrecords.List(context.Background(), vendor.KeyValues(), environment.TierProduction, "shop")
	if err != nil {
		t.Fatal(err)
	}
	for app, build := range promoted.Builds {
		var recorded []string
		for _, stack := range stacks {
			if stack.Kind == provider.StackApp && stack.App == app {
				recorded = append(recorded, stack.Build)
			}
		}
		if !slices.Equal(recorded, []string{build}) {
			t.Errorf("the promotion names build %q for %s, and its stack records %v, want the same identity string", build, app, recorded)
		}
	}
}
