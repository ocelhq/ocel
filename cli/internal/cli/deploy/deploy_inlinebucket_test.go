package deploy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
)

const inlineBucket = `bucket: { uploads: {
  endpoint: "https://abc.r2.cloudflarestorage.com", region: "auto", bucket: "acme", prefix: "uploads/",
  accessKeyId: { $env: "R2_KEY" }, secretAccessKey: { $env: "R2_SECRET" },
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
	t.Run("the record names the store and keeps its key out of the deploy request", func(t *testing.T) {
		run := setUpInline(t, inlineBucket)
		declareUploads(t, run.root)
		seedProduction(t, "R2_KEY", "AKIDEXAMPLE")
		seedProduction(t, "R2_SECRET", "r2-s3cret")

		out, err := run.deploy(t, deployOptions{})
		if err != nil {
			t.Fatalf("deploy: %v\n%s", err, out)
		}
		published := records(t)
		if len(published) != 1 || published[0].Name != "ocel:bucket.uploads" || published[0].Tier != environmentv1.Tier_TIER_PRODUCTION {
			t.Fatalf("records = %+v, want the inline bucket's record", published)
		}
		for _, want := range []string{`"endpoint":"https://abc.r2.cloudflarestorage.com"`, `"prefix":"uploads/"`, `"secretAccessKey":"r2-s3cret"`} {
			if !strings.Contains(published[0].Wire, want) {
				t.Errorf("record = %s, want it to contain %s", published[0].Wire, want)
			}
		}
		sent, err := os.ReadFile(run.journal)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(sent), "r2-s3cret") {
			t.Errorf("the deploy request contains the store's secret key: %s", sent)
		}
	})

	t.Run("a store that refuses the check stops the deploy before anything is kept", func(t *testing.T) {
		run := setUpInline(t, inlineBucket)
		declareUploads(t, run.root)
		seedProduction(t, "R2_KEY", "AKIDEXAMPLE")
		seedProduction(t, "R2_SECRET", "r2-s3cret")
		t.Setenv(clitest.FakeBucketRefusalEnvVar, "its CORS rules allow no request from https://acme.com")

		out, err := run.deploy(t, deployOptions{})
		if err == nil {
			t.Fatalf("deploy succeeded against a bucket the check refused\n%s", out)
		}
		if said := err.Error() + out; !strings.Contains(said, "bindings.bucket.uploads") || !strings.Contains(said, "https://acme.com") {
			t.Errorf("refusal = %q, want the binding and the difference named", said)
		}
		if published := records(t); len(published) != 0 {
			t.Errorf("records = %+v, want nothing kept", published)
		}
	})

	t.Run("what the check could not read is a warning, and the deploy goes on", func(t *testing.T) {
		run := setUpInline(t, inlineBucket)
		declareUploads(t, run.root)
		seedProduction(t, "R2_KEY", "AKIDEXAMPLE")
		seedProduction(t, "R2_SECRET", "r2-s3cret")
		t.Setenv(clitest.FakeBucketWarningEnvVar, "could not read the bucket's CORS rules (AccessDenied)")

		out, err := run.deploy(t, deployOptions{})
		if err != nil {
			t.Fatalf("deploy: %v\n%s", err, out)
		}
		if !strings.Contains(out, "could not read the bucket's CORS rules") {
			t.Errorf("output = %q, want the warning shown", out)
		}
	})
}
