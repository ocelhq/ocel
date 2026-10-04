package providerserver_test

import (
	"context"
	"slices"
	"strings"
	"sync"
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

func liveLine(message string) provider.LogEntry {
	return provider.LogEntry{Time: time.Now(), Message: message, Instance: "i-1"}
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

func (m missingLogs) Read(ctx context.Context, q provider.LogQuery, emit func([]provider.LogEntry) error, notice func(provider.LogNotice) error) error {
	if err := m.Logs.Read(ctx, q, emit, notice); err != nil {
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

func (refusingLogs) Read(context.Context, provider.LogQuery, func([]provider.LogEntry) error, func(provider.LogNotice) error) error {
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

type tailedLogs struct {
	stream *connect.ServerStreamForClient[contractv1.ReadLogsResponse]
	cancel context.CancelFunc
	t      *testing.T
}

func tailLogsOf(t *testing.T, client contractv1connect.ProviderServiceClient, req *contractv1.ReadLogsRequest) *tailedLogs {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	req.Tail = true
	stream, err := client.ReadLogs(ctx, req)
	if err != nil {
		t.Fatalf("ReadLogs() error = %v", err)
	}
	t.Cleanup(func() { stream.Close() })
	return &tailedLogs{stream: stream, cancel: cancel, t: t}
}

func (l *tailedLogs) next() *contractv1.ReadLogsResponse {
	l.t.Helper()
	received := make(chan bool, 1)
	go func() { received <- l.stream.Receive() }()
	select {
	case ok := <-received:
		if !ok {
			l.t.Fatalf("the stream ended with %v, want another message", l.stream.Err())
		}
	case <-time.After(5 * time.Second):
		l.t.Fatal("no message arrived within 5s")
	}
	return l.stream.Msg()
}

func (l *tailedLogs) nextKind() string {
	l.t.Helper()
	switch body := l.next().GetBody().(type) {
	case *contractv1.ReadLogsResponse_Batch:
		var messages []string
		for _, entry := range body.Batch.GetEntries() {
			messages = append(messages, entry.GetMessage())
		}
		return "batch:" + strings.Join(messages, ",")
	case *contractv1.ReadLogsResponse_Notice:
		return body.Notice.GetKind().String()
	}
	return "empty"
}

func (l *tailedLogs) historyUntilCaughtUp() []string {
	l.t.Helper()
	var messages []string
	for {
		switch body := l.next().GetBody().(type) {
		case *contractv1.ReadLogsResponse_Batch:
			for _, entry := range body.Batch.GetEntries() {
				messages = append(messages, entry.GetMessage())
			}
		case *contractv1.ReadLogsResponse_Notice:
			if body.Notice.GetKind() != contractv1.LogNotice_KIND_CAUGHT_UP {
				l.t.Fatalf("ReadLogs() sent the notice %s after the history %v, want CAUGHT_UP", body.Notice.GetKind(), messages)
			}
			return messages
		default:
			l.t.Fatalf("ReadLogs() sent an empty message after the history %v, want entries or CAUGHT_UP", messages)
		}
	}
}

func (l *tailedLogs) liveEntries(count int) []string {
	l.t.Helper()
	var messages []string
	for len(messages) < count {
		batch := l.next().GetBatch()
		if batch == nil {
			l.t.Fatalf("ReadLogs() sent a message other than entries after the live entries %v, want %d entries", messages, count)
		}
		for _, entry := range batch.GetEntries() {
			messages = append(messages, entry.GetMessage())
		}
	}
	if len(messages) > count {
		l.t.Fatalf("ReadLogs() sent the live entries %v, want %d", messages, count)
	}
	return messages
}

func (l *tailedLogs) endsWithin(deadline time.Duration) (error, bool) {
	l.t.Helper()
	ended := make(chan error, 1)
	go func() {
		for l.stream.Receive() {
		}
		ended <- l.stream.Err()
	}()
	select {
	case err := <-ended:
		return err, true
	case <-time.After(deadline):
		return nil, false
	}
}

func liveWeb(t *testing.T) (contractv1connect.ProviderServiceClient, *fake.Provider) {
	t.Helper()
	client, vendor := contractServed(t, "1.0.0")
	edgeProvisioned(t, vendor, environment.TierProduction, "shop")
	recordApps(t, vendor, environment.TierProduction, stackrecords.ProductionEnv, recordedApp{app: "web", seq: 0, functions: webFunction(0)})
	promoteBuilds(t, vendor, environment.TierProduction, router.DefaultPointer, map[string]string{"web": buildIdentity(0)})
	return client, vendor
}

func TestReadLogsSendsCaughtUpBeforeLiveEntries(t *testing.T) {
	t.Parallel()
	client, vendor := liveWeb(t)
	vendor.FakeLogs().Append("web-fn-a", logLine(time.Minute, "history"))

	tailed := tailLogsOf(t, client, logsRequest())
	if got, want := tailed.historyUntilCaughtUp(), []string{"history"}; !slices.Equal(got, want) {
		t.Fatalf("ReadLogs() sent %v before CAUGHT_UP, want the history %v", got, want)
	}
	vendor.FakeLogs().Append("web-fn-a", liveLine("live one"))
	vendor.FakeLogs().Append("web-fn-a", liveLine("live two"))
	if got, want := tailed.liveEntries(2), []string{"live one", "live two"}; !slices.Equal(got, want) {
		t.Errorf("ReadLogs() sent %v after CAUGHT_UP, want the live entries %v in order", got, want)
	}
}

func TestReadLogsStopsTailingWhenTheCallerCancels(t *testing.T) {
	t.Parallel()
	client, _ := liveWeb(t)

	tailed := tailLogsOf(t, client, logsRequest())
	if got := tailed.nextKind(); got != contractv1.LogNotice_KIND_CAUGHT_UP.String() {
		t.Fatalf("ReadLogs() sent %q first, want CAUGHT_UP", got)
	}
	tailed.cancel()
	if _, ended := tailed.endsWithin(time.Second); !ended {
		t.Error("ReadLogs() was still streaming 1s after the caller cancelled")
	}
}

func TestReadLogsEndsAfterCaughtUpWhenNotAskedToTail(t *testing.T) {
	t.Parallel()
	client, _ := liveWeb(t)

	read, err := readLogsOf(t, client, logsRequest())
	if err != nil {
		t.Fatalf("ReadLogs() error = %v", err)
	}
	if want := []string{contractv1.LogNotice_KIND_CAUGHT_UP.String()}; !slices.Equal(read.order, want) {
		t.Errorf("ReadLogs() without tail sent %v, want %v and then an end", read.order, want)
	}
}

func TestReadLogsKeepsTheStreamOpenWhenNothingIsDeployedToTail(t *testing.T) {
	t.Parallel()
	client, _ := contractServed(t, "1.0.0")

	tailed := tailLogsOf(t, client, logsRequest())
	if got := tailed.nextKind(); got != contractv1.LogNotice_KIND_CAUGHT_UP.String() {
		t.Fatalf("ReadLogs() sent %q, want CAUGHT_UP", got)
	}
	if err, ended := tailed.endsWithin(200 * time.Millisecond); ended {
		t.Fatalf("ReadLogs() of nothing ended with %v, want the stream held open until the caller cancels", err)
	}
}

type spyingLogs struct {
	*fake.Provider
	gone        []string
	refuseTail  error
	endTail     bool
	mu          sync.Mutex
	queriesRead []provider.LogQuery
}

func (s *spyingLogs) Logs() provider.Logs { return spiedLogs{s} }

func (s *spyingLogs) queries() []provider.LogQuery {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.queriesRead)
}

type spiedLogs struct{ spy *spyingLogs }

func (l spiedLogs) Read(ctx context.Context, q provider.LogQuery, emit func([]provider.LogEntry) error, notice func(provider.LogNotice) error) error {
	l.spy.mu.Lock()
	l.spy.queriesRead = append(l.spy.queriesRead, q)
	l.spy.mu.Unlock()
	if q.Tail && l.spy.refuseTail != nil {
		return l.spy.refuseTail
	}
	if q.Tail && l.spy.endTail {
		return nil
	}
	if err := l.spy.Provider.Logs().Read(ctx, q, emit, notice); err != nil {
		return err
	}
	var missing provider.LogTargetsMissing
	for _, target := range q.Targets {
		if !q.Tail && slices.Contains(l.spy.gone, target.Physical()) {
			missing.Targets = append(missing.Targets, target)
		}
	}
	if len(missing.Targets) > 0 {
		return missing
	}
	return nil
}

func spiedWeb(t *testing.T, spy *spyingLogs) contractv1connect.ProviderServiceClient {
	t.Helper()
	client := servedProvider(t, "1.0.0", spy)
	edgeProvisioned(t, spy.Provider, environment.TierProduction, "shop")
	recordApps(t, spy.Provider, environment.TierProduction, stackrecords.ProductionEnv,
		recordedApp{app: "web", seq: 0, functions: webFunction(0)},
		recordedApp{app: "web", seq: 1, functions: webFunction(1)},
	)
	promoteBuilds(t, spy.Provider, environment.TierProduction, router.DefaultPointer, map[string]string{"web": buildIdentity(1)})
	return client
}

func TestReadLogsTailsFromBeforeWhereTheHistoryStopped(t *testing.T) {
	t.Parallel()
	spy := &spyingLogs{Provider: fake.NewProvider(fake.Options{Region: "nowhere"}).WithProjectDir(workingDir(t))}
	client := spiedWeb(t, spy)

	tailed := tailLogsOf(t, client, logsRequest())
	if got := tailed.nextKind(); got != contractv1.LogNotice_KIND_CAUGHT_UP.String() {
		t.Fatalf("ReadLogs() sent %q, want CAUGHT_UP", got)
	}
	var history, live provider.LogQuery
	deadline := time.Now().Add(5 * time.Second)
	for len(spy.queries()) < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	queries := spy.queries()
	if len(queries) != 2 {
		t.Fatalf("the provider read %d times, want a history read and a tail", len(queries))
	}
	history, live = queries[0], queries[1]
	if history.Tail || !live.Tail {
		t.Errorf("the reads had Tail %v then %v, want the history first and the tail second", history.Tail, live.Tail)
	}
	if history.Until.IsZero() || !live.Since.Before(history.Until) {
		t.Errorf("history ended at %s and the tail began at %s, want the tail to reach back before it so an entry the store took late is still read", history.Until, live.Since)
	}
	if !live.Until.IsZero() {
		t.Errorf("the tail has Until %s, want none: it ends with its context", live.Until)
	}
	if len(live.Targets) != 1 || live.Targets[0].Physical() != "web-fn-b" {
		t.Errorf("the tail read %+v, want the live release's function alone", live.Targets)
	}
}

func TestReadLogsTailsOnlyTheTargetsWhoseLogsStillExist(t *testing.T) {
	t.Parallel()
	spy := &spyingLogs{Provider: fake.NewProvider(fake.Options{Region: "nowhere"}).WithProjectDir(workingDir(t)), gone: []string{"web-fn-a"}}
	client := spiedWeb(t, spy)

	req := logsRequest()
	req.AllReleases = true
	tailed := tailLogsOf(t, client, req)
	want := []string{contractv1.LogNotice_KIND_SOURCE_GONE.String(), contractv1.LogNotice_KIND_CAUGHT_UP.String()}
	for _, kind := range want {
		if got := tailed.nextKind(); got != kind {
			t.Fatalf("ReadLogs() sent %q, want %q in the order %v", got, kind, want)
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(spy.queries()) < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	queries := spy.queries()
	if len(queries) != 2 || len(queries[1].Targets) != 1 || queries[1].Targets[0].Physical() != "web-fn-b" {
		t.Errorf("the provider was asked to read %+v, want the tail of the one release whose logs exist", queries)
	}
}

func TestReadLogsRefusesATailThatHasAnEndTime(t *testing.T) {
	t.Parallel()
	client, _ := liveWeb(t)

	req := logsRequest()
	req.Tail = true
	req.Until = timestamppb.New(logsEpoch)
	_, err := readLogsOf(t, client, req)
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("ReadLogs() of a tail with an until error = %v, want an invalid-argument refusal", err)
	}
}

func tailOpened(t *testing.T, vendor *fake.Provider) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := vendor.FakeLogs().WaitForTail(ctx); err != nil {
		t.Fatalf("the provider's tail was not opened after CAUGHT_UP: %v", err)
	}
}

func TestReadLogsPassesTheNoticesOfATailOn(t *testing.T) {
	t.Parallel()
	client, vendor := liveWeb(t)

	tailed := tailLogsOf(t, client, logsRequest())
	if got := tailed.nextKind(); got != contractv1.LogNotice_KIND_CAUGHT_UP.String() {
		t.Fatalf("ReadLogs() sent %q, want CAUGHT_UP", got)
	}
	tailOpened(t, vendor)
	vendor.FakeLogs().SendNotice(provider.LogNotice{Kind: provider.LogSampled, Omitted: 7})
	sampled := tailed.next().GetNotice()
	if sampled.GetKind() != contractv1.LogNotice_KIND_SAMPLED || sampled.GetOmitted() != 7 || sampled.GetMessage() == "" {
		t.Errorf("a sampled notice reached the caller as %v, want KIND_SAMPLED omitting 7 with a message", sampled)
	}
	vendor.FakeLogs().SendNotice(provider.LogNotice{Kind: provider.LogReconnected})
	if reconnected := tailed.next().GetNotice(); reconnected.GetKind() != contractv1.LogNotice_KIND_RECONNECTED || reconnected.GetMessage() == "" {
		t.Errorf("a reconnected notice reached the caller as %v, want KIND_RECONNECTED with a message", reconnected)
	}
}

func TestReadLogsStopsWhenTheProviderRefusesTheTail(t *testing.T) {
	t.Parallel()
	spy := &spyingLogs{
		Provider:   fake.NewProvider(fake.Options{Region: "nowhere"}).WithProjectDir(workingDir(t)),
		refuseTail: refusal.Refuse(refusal.CodeNotReady, "the log stream is not reachable"),
	}
	client := spiedWeb(t, spy)

	tailed := tailLogsOf(t, client, logsRequest())
	if got := tailed.nextKind(); got != contractv1.LogNotice_KIND_CAUGHT_UP.String() {
		t.Fatalf("ReadLogs() sent %q, want CAUGHT_UP before the tail was tried", got)
	}
	err, ended := tailed.endsWithin(time.Second)
	if !ended || connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("ReadLogs() after a refused tail = %v (ended %v), want the provider's refusal passed on", err, ended)
	}
}

func TestReadLogsKeepsTheStreamOpenWhenTheProvidersTailEndsBeforeTheCallerCancels(t *testing.T) {
	t.Parallel()
	spy := &spyingLogs{Provider: fake.NewProvider(fake.Options{Region: "nowhere"}).WithProjectDir(workingDir(t)), endTail: true}
	client := spiedWeb(t, spy)

	tailed := tailLogsOf(t, client, logsRequest())
	if got := tailed.nextKind(); got != contractv1.LogNotice_KIND_CAUGHT_UP.String() {
		t.Fatalf("ReadLogs() sent %q, want CAUGHT_UP", got)
	}
	if err, ended := tailed.endsWithin(200 * time.Millisecond); ended {
		t.Fatalf("ReadLogs() ended with %v when the provider had nothing more to tail, want the stream held open until the caller cancels", err)
	}
}

func TestReadLogsNamesTheReleaseOfASourceThatIsGoneWhileTailing(t *testing.T) {
	t.Parallel()
	client, vendor := liveWeb(t)
	build, err := provider.ParseBuild(buildIdentity(0))
	if err != nil {
		t.Fatal(err)
	}

	tailed := tailLogsOf(t, client, logsRequest())
	if got := tailed.nextKind(); got != contractv1.LogNotice_KIND_CAUGHT_UP.String() {
		t.Fatalf("ReadLogs() sent %q, want CAUGHT_UP", got)
	}
	tailOpened(t, vendor)
	vendor.FakeLogs().SendNotice(provider.LogNotice{Kind: provider.LogSourceGone, Target: provider.LogTarget{App: "web", Source: "http", Release: build.Release().String()}})
	gone := tailed.next().GetNotice()
	if gone.GetKind() != contractv1.LogNotice_KIND_SOURCE_GONE || !strings.Contains(gone.GetMessage(), build.Release().String()) || !strings.Contains(gone.GetMessage(), "web") {
		t.Errorf("a source gone while tailing reached the caller as %v, want KIND_SOURCE_GONE naming its app and release %s", gone, build.Release())
	}
}

func TestReadLogsFailsOnANoticeItCannotName(t *testing.T) {
	t.Parallel()
	client, vendor := liveWeb(t)

	tailed := tailLogsOf(t, client, logsRequest())
	if got := tailed.nextKind(); got != contractv1.LogNotice_KIND_CAUGHT_UP.String() {
		t.Fatalf("ReadLogs() sent %q, want CAUGHT_UP", got)
	}
	tailOpened(t, vendor)
	vendor.FakeLogs().SendNotice(provider.LogNotice{Kind: provider.LogNoticeKind(99)})
	err, ended := tailed.endsWithin(time.Second)
	if !ended || err == nil {
		t.Errorf("ReadLogs() after a notice of an unknown kind = %v (ended %v), want the stream to fail rather than drop it", err, ended)
	}
}

type replayingLogs struct {
	*fake.Provider
	history, tail []provider.LogEntry
}

func (r replayingLogs) Logs() provider.Logs { return r }

func (r replayingLogs) Read(ctx context.Context, q provider.LogQuery, emit func([]provider.LogEntry) error, _ func(provider.LogNotice) error) error {
	read := r.history
	if q.Tail {
		read = r.tail
	}
	if err := emit(read); err != nil {
		return err
	}
	if q.Tail {
		<-ctx.Done()
	}
	return nil
}

func TestReadLogsSendsAnEntryThatBothHistoryAndTheTailReadOnce(t *testing.T) {
	t.Parallel()
	vendor := fake.NewProvider(fake.Options{Region: "nowhere"}).WithProjectDir(workingDir(t))
	both := provider.LogEntry{ID: "both", Time: time.Now(), Message: "at the boundary"}
	client := servedProvider(t, "1.0.0", replayingLogs{
		Provider: vendor,
		history:  []provider.LogEntry{both},
		tail:     []provider.LogEntry{both, {ID: "after", Time: time.Now(), Message: "after"}},
	})
	edgeProvisioned(t, vendor, environment.TierProduction, "shop")
	recordApps(t, vendor, environment.TierProduction, stackrecords.ProductionEnv, recordedApp{app: "web", seq: 0, functions: webFunction(0)})
	promoteBuilds(t, vendor, environment.TierProduction, router.DefaultPointer, map[string]string{"web": buildIdentity(0)})

	tailed := tailLogsOf(t, client, logsRequest())
	if got, want := tailed.historyUntilCaughtUp(), []string{"at the boundary"}; !slices.Equal(got, want) {
		t.Fatalf("ReadLogs() sent %v before CAUGHT_UP, want the history %v", got, want)
	}
	if got, want := tailed.liveEntries(1), []string{"after"}; !slices.Equal(got, want) {
		t.Errorf("ReadLogs() sent %v after CAUGHT_UP, want %v: the entry history sent once and not again", got, want)
	}
}

func TestReadLogsSendsAnEntryTheStoreTookAfterHistoryWasRead(t *testing.T) {
	t.Parallel()
	client, vendor := liveWeb(t)

	tailed := tailLogsOf(t, client, logsRequest())
	if got := tailed.nextKind(); got != contractv1.LogNotice_KIND_CAUGHT_UP.String() {
		t.Fatalf("ReadLogs() sent %q, want CAUGHT_UP", got)
	}
	vendor.FakeLogs().Append("web-fn-a", provider.LogEntry{Time: time.Now().Add(-5 * time.Second), Message: "taken late"})
	if got := tailed.liveEntries(1); !slices.Equal(got, []string{"taken late"}) {
		t.Errorf("ReadLogs() sent %v, want the entry stamped before the tail began that the store took after history was read", got)
	}
}

func TestReadLogsTailsNothingOlderThanTheHistoryItCutToTheLimit(t *testing.T) {
	t.Parallel()
	client, vendor := liveWeb(t)
	vendor.FakeLogs().Append("web-fn-a", provider.LogEntry{Time: time.Now().Add(-3 * time.Second), Message: "cut"})
	vendor.FakeLogs().Append("web-fn-a", provider.LogEntry{Time: time.Now().Add(-2 * time.Second), Message: "kept"})

	req := logsRequest()
	req.Limit = 1
	tailed := tailLogsOf(t, client, req)
	if got, want := tailed.historyUntilCaughtUp(), []string{"kept"}; !slices.Equal(got, want) {
		t.Fatalf("ReadLogs() sent %v before CAUGHT_UP, want the history %v", got, want)
	}
	vendor.FakeLogs().Append("web-fn-a", liveLine("live"))
	if got, want := tailed.liveEntries(1), []string{"live"}; !slices.Equal(got, want) {
		t.Errorf("ReadLogs() sent %v after CAUGHT_UP, want %v and nothing older than the history it cut", got, want)
	}
}

func TestReadLogsTailsNothingStampedBeforeTheSinceTheCallerAskedFor(t *testing.T) {
	t.Parallel()
	client, vendor := liveWeb(t)

	req := logsRequest()
	req.Since = timestamppb.New(time.Now().Add(-5 * time.Second))
	tailed := tailLogsOf(t, client, req)
	if got := tailed.nextKind(); got != contractv1.LogNotice_KIND_CAUGHT_UP.String() {
		t.Fatalf("ReadLogs() sent %q, want CAUGHT_UP", got)
	}
	vendor.FakeLogs().Append("web-fn-a", provider.LogEntry{Time: time.Now().Add(-10 * time.Second), Message: "before since"})
	vendor.FakeLogs().Append("web-fn-a", liveLine("after since"))
	if got := tailed.liveEntries(1); !slices.Equal(got, []string{"after since"}) {
		t.Errorf("ReadLogs() sent %v, want only what is stamped from the caller's since on", got)
	}
}
