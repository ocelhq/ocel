package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cloud/edge"
)

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

	blocked := &recordingRootStack{}
	failing := Config{AssetBucket: "assets-xyz", Uploader: &fakeUploader{putErr: errors.New("access denied")}}
	if _, err := finalizeDeploy(context.Background(), failing, blocked, specs, nil, "promo2", "", "", 100, results); err == nil {
		t.Fatal("the deploy cut over despite having no origin record to revalidate against")
	}
	if len(blocked.staged) != 0 || len(blocked.promotions) != 0 {
		t.Errorf("staged %d over %d promotions after a failed record write, want neither", len(blocked.staged), len(blocked.promotions))
	}
}
