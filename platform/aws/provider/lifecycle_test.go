package provider_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/cloudformation"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
)

const lifecycleSlug = "ocel-aws-e2e"

const patience = 15 * time.Minute

type journey struct {
	account  account
	bin      string
	project  string
	settings string
	cache    string
}

func lifecycle(t *testing.T) journey {
	t.Helper()
	a := live(t)

	dir := t.TempDir()
	run := journey{
		account:  a,
		bin:      filepath.Join(dir, "ocel"),
		project:  filepath.Join(dir, "project"),
		settings: filepath.Join(dir, "config"),
		cache:    filepath.Join(dir, "cache"),
	}
	if err := os.MkdirAll(run.project, 0o700); err != nil {
		t.Fatal(err)
	}

	root := repoRoot(t)
	build(t, filepath.Join(root, "cli"), run.bin, "./ocel")
	build(t, ".", filepath.Join(run.installed(), "bin", "deploy"), "./cmd/deploy")
	write(t, filepath.Join(run.installed(), "package.json"), run.manifest(t))
	write(t, filepath.Join(run.project, "ocel.config.ts"), run.declaration(t))

	return run
}

func (j journey) platformPackage() string {
	arch := runtime.GOARCH
	if arch == "amd64" {
		arch = "x64"
	}
	return "@ocel/provider-aws-" + runtime.GOOS + "-" + arch
}

func (j journey) installed() string {
	return filepath.Join(j.project, "node_modules", filepath.FromSlash(j.platformPackage()))
}

func (j journey) manifest(t *testing.T) string {
	t.Helper()
	written, err := json.Marshal(map[string]any{
		"name":    j.platformPackage(),
		"version": "0.0.0",
		"bin":     map[string]any{"ocel-provider-aws-deploy": "bin/deploy"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(written) + "\n"
}

func (j journey) declaration(t *testing.T) string {
	t.Helper()
	options, err := json.Marshal(map[string]any{
		"package": "@ocel/provider-aws",
		"options": map[string]any{"region": liveRegion},
	})
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("export default {\n  slug: %q,\n  provider: %s,\n};\n", lifecycleSlug, options)
}

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func build(t *testing.T, module, out, pkg string) {
	t.Helper()
	made := exec.Command("go", "build", "-C", module, "-o", out, pkg)
	made.Env = append(os.Environ(), "GOCACHEPROG=")
	if rendered, err := made.CombinedOutput(); err != nil {
		t.Fatalf("go build -C %s -o %s %s: %v\n%s\nthe CLI carries an embedded node bundle: `pnpm install --frozen-lockfile && pnpm --filter ocel build && go generate ./...` in cli/ builds it",
			module, out, pkg, err, rendered)
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (j journey) env() []string {
	var kept []string
	for _, entry := range os.Environ() {
		switch name, _, _ := strings.Cut(entry, "="); name {
		case "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "OCEL_CONFIG", "OCEL_ACCESS_TOKEN":
			continue
		}
		kept = append(kept, entry)
	}
	return append(kept,
		"XDG_CONFIG_HOME="+j.settings,
		"XDG_CACHE_HOME="+j.cache,
	)
}

func (j journey) run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	ctx, done := context.WithTimeout(context.Background(), patience)
	defer done()

	cmd := exec.CommandContext(ctx, j.bin, args...)
	cmd.Dir = j.project
	cmd.Env = j.env()
	var rendered bytes.Buffer
	cmd.Stdout = &rendered
	cmd.Stderr = &rendered
	err := cmd.Run()
	if ctx.Err() != nil {
		err = fmt.Errorf("ocel %s was still running after %s: %w", strings.Join(args, " "), patience, ctx.Err())
	}
	return plain(rendered.String()), err
}

func (j journey) must(t *testing.T, args ...string) string {
	t.Helper()
	rendered, err := j.run(t, args...)
	if err != nil {
		t.Fatalf("ocel %s = %v\n%s", strings.Join(args, " "), err, rendered)
	}
	return rendered
}

var escapes = regexp.MustCompile("\x1b\\[[\x30-\x3f]*[\x20-\x2f]*[\x40-\x7e]|\x1b\\][^\x07\x1b]*(\x07|\x1b\\\\)|\x1b[()][0-9A-B]|\x1b[=>]")

func plain(rendered string) string {
	return strings.ReplaceAll(escapes.ReplaceAllString(rendered, ""), "\r\n", "\n")
}

func TestLifecycleTheWholeBootstrapRunsOnTheRealBinaryAndGivesTheAccountBack(t *testing.T) {
	run := lifecycle(t)
	class := providerkit.ClassProduction
	run.account.emptied(t, class)
	ctx := context.Background()

	fresh := run.must(t, "doctor")
	if !strings.Contains(fresh, "not set up — run `ocel bootstrap production`") {
		t.Fatalf("`ocel doctor` on an account nothing has written to said:\n%s", fresh)
	}

	applied := run.must(t, "bootstrap", "production", "--yes")
	if !strings.Contains(applied, "Bootstrapped") {
		t.Errorf("`ocel bootstrap production --yes` finished without saying it bootstrapped:\n%s", applied)
	}
	if status := run.account.stackStatus(t, bootstrap.StackName); status != "CREATE_COMPLETE" {
		t.Fatalf("%s stands at %q after the CLI applied it, want CREATE_COMPLETE", bootstrap.StackName, status)
	}
	held, err := bootstrap.CheckDeployedFor(ctx, cloudformation.NewFromConfig(run.account.aws), string(class))
	if err != nil {
		t.Fatal(err)
	}
	for _, bucket := range []string{held.StateBucket, held.ArtifactBucket, held.AssetBucket} {
		if !run.account.bucketStands(t, bucket) {
			t.Errorf("%s is named by the stack the CLI applied but no bucket answers for it", bucket)
		}
	}
	if !run.account.paramStands(t, bootstrap.PassphraseParamName) {
		t.Errorf("%s is missing after the CLI bootstrapped, and every Pulumi stack this account deploys is encrypted under it", bootstrap.PassphraseParamName)
	}

	standing := run.must(t, "doctor")
	if !strings.Contains(standing, "bootstrapped — schema") {
		t.Fatalf("`ocel doctor` after an apply still calls production unbootstrapped:\n%s", standing)
	}

	replanned := run.must(t, "bootstrap", "production", "--dry")
	if !strings.Contains(replanned, "No infrastructure changes") {
		t.Errorf("a re-plan over a bootstrapped account found work to do:\n%s", replanned)
	}
	destroyed := run.must(t, "bootstrap", "destroy", "production", "--yes")
	for _, unrecoverable := range []string{"StateBucket", "VarsTable"} {
		if !strings.Contains(destroyed, unrecoverable) {
			t.Errorf("`ocel bootstrap destroy production` never named %s, and a user confirms without knowing what is unrecoverable:\n%s", unrecoverable, destroyed)
		}
	}

	gone := run.must(t, "doctor")
	if !strings.Contains(gone, "not set up — run `ocel bootstrap production`") {
		t.Errorf("`ocel doctor` after a destroy still claims a bootstrap:\n%s", gone)
	}
	if status := run.account.stackStatus(t, bootstrap.StackName); status != "" && status != "DELETE_COMPLETE" {
		t.Errorf("%s stands at %q after a destroy, so the account was not given back", bootstrap.StackName, status)
	}
	for _, bucket := range []string{held.StateBucket, held.ArtifactBucket, held.AssetBucket} {
		if run.account.bucketStands(t, bucket) {
			t.Errorf("%s still answers after a destroy, so the account was not given back", bucket)
		}
	}
	if run.account.paramStands(t, bootstrap.PassphraseParamName) {
		t.Errorf("%s stands after the last class on this account went", bootstrap.PassphraseParamName)
	}
}
