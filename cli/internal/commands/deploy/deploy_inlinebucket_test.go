package deploy

import (
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/ocelhq/ocel/cli/internal/clitest"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
)

const inlineBucket = `bucket: { uploads: {
  endpoint: "https://abc.storage.example.com", region: "auto", bucket: "acme", prefix: "uploads/",
  accessKeyId: { $env: "R2_KEY" }, secretAccessKey: { $env: "BUCKET_SECRET" },
} }`

func declareUploads(t *testing.T, root string) {
	t.Helper()
	clitest.WriteFile(t, filepath.Join(root, "shared", "files.ts"), `
import { declareBucket } from "./declare.js";

export const files = declareBucket("uploads");
`)
	clitest.WriteFile(t, filepath.Join(root, "shared", "index.ts"), `
export * from "./db.js";
export * from "./files.js";
`)
	clitest.WriteFile(t, filepath.Join(root, "apps", "api", "src", "server.ts"), `
import { db, files } from "../../../shared/index.js";

export function handler() {
  return db.name + files.name;
}
`)
}

func TestDeployBindsAnInlineBucket(t *testing.T) {
	run := setUpInline(t, inlineBucket)
	declareUploads(t, run.root)
	seedProduction(t, "R2_KEY", "AKIDEXAMPLE")
	seedProduction(t, "BUCKET_SECRET", "bucket-s3cret")

	out, err := run.deploy(t, deployOptions{})
	if err != nil {
		t.Fatalf("deploy: %v\n%s", err, out)
	}
	carried := sentRequest(t, run.journal).GetInlineBindings()
	if len(carried) != 1 || carried[0].GetName() != "ocel:bucket.uploads" {
		t.Fatalf("inline bindings = %v, want the inline bucket's record", carriedNames(carried))
	}
	want := &bindingsv1.BucketProperties{
		Endpoint: "https://abc.storage.example.com", Region: "auto", Bucket: "acme", Prefix: "uploads/",
		AccessKeyId: "AKIDEXAMPLE", SecretAccessKey: "bucket-s3cret",
	}
	if got := carried[0].GetBucket(); !proto.Equal(got, want) {
		t.Errorf("bucket = %s at %s under %q, want %s at %s under %q with the key pair the variables hold",
			got.GetBucket(), got.GetEndpoint(), got.GetPrefix(), want.GetBucket(), want.GetEndpoint(), want.GetPrefix())
	}
	if strings.Contains(out, "bucket-s3cret") {
		t.Errorf("the deploy printed the store's secret key: %s", out)
	}
}
