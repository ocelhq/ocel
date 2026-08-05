package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cloud/edge"
)

// originStore points a Config at a recording uploader, so a test can assert what
// the origin-record write published and in what order relative to the cutover.
func originStore(cfg Config) Config {
	cfg.AssetBucket = "assets-xyz"
	cfg.Uploader = &fakeUploader{}
	return cfg
}

func isrResult(app, prefix string, urls map[string]string) appDeployResult {
	return appDeployResult{
		App:      app,
		Identity: buildOnly("B1"),
		Record:   edge.DeploymentRecord{App: app, IsrPrefix: prefix, FunctionURLs: urls},
	}
}

// TestWriteOriginRecords_PublishesWhereTheConsumerLooks. The consumer reads
// <isrPrefix>/origin.json out of the provider's own asset bucket — the one its
// role is granted on — and resolves the route it was asked to render from the
// map inside. Both halves of that key are this write's contract.
func TestWriteOriginRecords_PublishesWhereTheConsumerLooks(t *testing.T) {
	up := &fakeUploader{}
	cfg := Config{AssetBucket: "assets-xyz", Uploader: up}
	urls := map[string]string{"/": "https://web1.lambda-url.eu-west-1.on.aws/"}

	if err := writeOriginRecords(context.Background(), cfg, []appDeployResult{
		isrResult("web", "prod/proj/web/B1", urls),
	}); err != nil {
		t.Fatalf("writeOriginRecords: %v", err)
	}

	const key = "prod/proj/web/B1/origin.json"
	if len(up.puts) != 1 || up.puts[0] != key {
		t.Fatalf("published %v, want exactly %q", up.puts, key)
	}
	if up.buckets[0] != "assets-xyz" {
		t.Errorf("published into %q, want the provider's own asset bucket; the consumer's read grant reaches no other", up.buckets[0])
	}
	if got := up.contentTypes[key]; got != "application/json" {
		t.Errorf("ContentType = %q, want application/json", got)
	}

	var record originRecord
	if err := json.Unmarshal([]byte(up.putBodies[key]), &record); err != nil {
		t.Fatalf("record is not JSON: %v", err)
	}
	if record.Version != originRecordVersion {
		t.Errorf("v = %d, want %d; the consumer rejects a version it does not know", record.Version, originRecordVersion)
	}
	if got := record.FunctionURLs["/"]; got != urls["/"] {
		t.Errorf("functionUrls[/] = %q, want %q", got, urls["/"])
	}
}

// TestWriteOriginRecords_SkipsAppsWithNothingToRevalidate. An app whose
// framework keeps no server-side cache has no ISR prefix and nothing to
// revalidate, and an app whose stack failed is about to abort the promote.
func TestWriteOriginRecords_SkipsAppsWithNothingToRevalidate(t *testing.T) {
	up := &fakeUploader{}
	cfg := Config{AssetBucket: "assets-xyz", Uploader: up}
	failed := isrResult("api", "prod/proj/api/B1", map[string]string{"/": "https://api1.lambda-url.eu-west-1.on.aws/"})
	failed.Err = errors.New("stack failed")

	if err := writeOriginRecords(context.Background(), cfg, []appDeployResult{
		isrResult("static", "", nil),
		failed,
	}); err != nil {
		t.Fatalf("writeOriginRecords: %v", err)
	}
	if len(up.puts) != 0 {
		t.Errorf("published %v, want nothing", up.puts)
	}
}

// TestWriteOriginRecords_FailureFailsTheDeployLoudly. Swallowing this is the
// epic's signature failure exactly: the record is absent, the edge enqueues, the
// send succeeds, the refresh thunk reports landed, the colo sentinel re-arms,
// the consumer answers origin-unusable for every route of the build, and the
// deploy output said nothing was wrong. There is no partial success to report.
func TestWriteOriginRecords_FailureFailsTheDeployLoudly(t *testing.T) {
	cfg := Config{AssetBucket: "assets-xyz", Uploader: &fakeUploader{putErr: errors.New("access denied")}}
	err := writeOriginRecords(context.Background(), cfg, []appDeployResult{
		isrResult("web", "prod/proj/web/B1", map[string]string{"/": "https://web1.lambda-url.eu-west-1.on.aws/"}),
	})
	if err == nil {
		t.Fatal("a failed origin-record write was swallowed; that build would go live and never revalidate")
	}
	for _, want := range []string{"prod/proj/web/B1/origin.json", "access denied", "web"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
}

// TestFinalizeDeploy_PublishesTheOriginRecordBeforeCuttingOver is the ordering
// requirement, and it is the whole point of where this write sits. A build that
// is staged and promoted before its record exists is live with routes that
// enqueue a revalidation and never receive one — and the window is not
// theoretical, it is however long the deploy takes to notice.
func TestFinalizeDeploy_PublishesTheOriginRecordBeforeCuttingOver(t *testing.T) {
	up := &fakeUploader{}
	cfg := Config{AssetBucket: "assets-xyz", Uploader: up}
	fake := &recordingRootStack{}

	specs := []edge.RootStackSpec{{Version: "v1"}}
	results := []appDeployResult{isrResult("web", "prod/proj/web/B1", map[string]string{"/": "https://web1.lambda-url.eu-west-1.on.aws/"})}
	if _, err := finalizeDeploy(context.Background(), cfg, fake, specs, nil, "promo1", "", "", 100, results); err != nil {
		t.Fatalf("finalizeDeploy: %v", err)
	}
	if len(up.puts) != 1 {
		t.Fatalf("published %v, want the one origin record", up.puts)
	}
	if len(fake.staged) != 1 || len(fake.promotions) != 1 {
		t.Fatalf("staged %d over %d promotions, want 1 and 1", len(fake.staged), len(fake.promotions))
	}

	// A write that fails takes the cutover with it: nothing is staged and
	// nothing is promoted.
	blocked := &recordingRootStack{}
	failing := Config{AssetBucket: "assets-xyz", Uploader: &fakeUploader{putErr: errors.New("access denied")}}
	if _, err := finalizeDeploy(context.Background(), failing, blocked, specs, nil, "promo2", "", "", 100, results); err == nil {
		t.Fatal("the deploy cut over despite having no origin record to revalidate against")
	}
	if len(blocked.staged) != 0 || len(blocked.promotions) != 0 {
		t.Errorf("staged %d over %d promotions after a failed record write, want neither", len(blocked.staged), len(blocked.promotions))
	}
}
