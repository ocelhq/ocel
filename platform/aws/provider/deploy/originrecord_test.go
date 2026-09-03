package deploy

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

func TestTheReleasePublishesTheOriginRecordTheRevalidatorResolvesBy(t *testing.T) {
	t.Parallel()
	up := &fakeUploader{}
	cfg := Config{AssetBucket: "assets-xyz", Uploader: up}
	work := &appWork{
		cache: &isrConfig{Bucket: "assets-xyz", Prefix: "prod/proj/web/B1/isr"},
		functions: appStackFunctions{Functions: []appFunction{
			{Logical: "fn--web--bundle-0", RouteID: "bundle-0"},
			{Logical: "fn--web--api", RouteID: "api"},
		}},
	}
	result := providerkit.StackResult{Functions: []providerkit.Function{
		{Name: "fn--web--bundle-0", URL: "https://web1.lambda-url.eu-west-1.on.aws/"},
		{Name: "fn--web--api", URL: "https://api1.lambda-url.eu-west-1.on.aws/"},
	}}

	if err := writeOriginRecord(context.Background(), cfg, "web", work, result); err != nil {
		t.Fatalf("writeOriginRecord: %v", err)
	}

	const key = "prod/proj/web/B1/isr/origin.json"
	if len(up.puts) != 1 || up.puts[0] != key {
		t.Fatalf("published %v, want exactly %q: the revalidator reads the record beside the ISR prefix the message names", up.puts, key)
	}
	if up.buckets[0] != "assets-xyz" {
		t.Errorf("published into %q, want the asset bucket the revalidator's read grant reaches", up.buckets[0])
	}
	if got := up.contentTypes[key]; got != "application/json" {
		t.Errorf("ContentType = %q, want application/json", got)
	}
	var record originRecord
	if err := json.Unmarshal([]byte(up.putBodies[key]), &record); err != nil {
		t.Fatalf("record is not JSON: %v", err)
	}
	if record.Version != originRecordVersion {
		t.Errorf("v = %d, want %d; the revalidator rejects a version it does not know", record.Version, originRecordVersion)
	}
	if got := record.FunctionURLs["bundle-0"]; got != "https://web1.lambda-url.eu-west-1.on.aws/" {
		t.Errorf("functionUrls[bundle-0] = %q, want the URL keyed by the route id a revalidation message names", got)
	}
	if _, keyed := record.FunctionURLs["fn--web--bundle-0"]; keyed {
		t.Errorf("functionUrls = %v, want no entry under the logical name", record.FunctionURLs)
	}
}

func TestAReleaseWithNoISRCachePublishesNoOriginRecord(t *testing.T) {
	t.Parallel()
	up := &fakeUploader{}
	cfg := Config{AssetBucket: "assets-xyz", Uploader: up}

	if err := writeOriginRecord(context.Background(), cfg, "static", &appWork{}, providerkit.StackResult{}); err != nil {
		t.Fatalf("writeOriginRecord: %v", err)
	}
	if len(up.puts) != 0 {
		t.Errorf("published %v, want nothing: an app that never revalidates has no origin for the revalidator to reach", up.puts)
	}
}
