package aws_test

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
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/cloudformation"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
)

const lifecycleSlug = "ocel-aws-e2e"

const patience = 15 * time.Minute

type journey struct {
	account   account
	bin       string
	project   string
	settings  string
	cache     string
	providers string
}

const unreleasedVersion = "dev"

func lifecycle(t *testing.T) journey {
	t.Helper()
	a := live(t)

	dir := t.TempDir()
	run := journey{
		account:   a,
		bin:       filepath.Join(dir, "ocel"),
		project:   filepath.Join(dir, "project"),
		settings:  filepath.Join(dir, "config"),
		cache:     filepath.Join(dir, "cache"),
		providers: filepath.Join(dir, "providers"),
	}
	if err := os.MkdirAll(run.project, 0o700); err != nil {
		t.Fatal(err)
	}

	root := repoRoot(t)
	build(t, filepath.Join(root, "cli"), run.bin, "./ocel")
	if prebuilt := os.Getenv(prebuiltProviderEnv); prebuilt != "" {
		binary, err := os.ReadFile(prebuilt)
		if err != nil {
			t.Fatal(err)
		}
		write(t, run.installed(), string(binary))
		if err := os.Chmod(run.installed(), 0o700); err != nil {
			t.Fatal(err)
		}
	} else {
		build(t, ".", run.installed(), "./cmd/deploy")
	}
	write(t, filepath.Join(run.project, "ocel.config.ts"), run.declaration(t))

	return run
}

func (j journey) installed() string {
	return filepath.Join(j.providers, "provider", "aws", unreleasedVersion, runtime.GOOS+"-"+runtime.GOARCH, "provider-aws")
}

func (j journey) declaration(t *testing.T) string {
	t.Helper()
	options, err := json.Marshal(map[string]any{
		"aws": map[string]any{"region": liveRegion},
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
	if err := os.MkdirAll(filepath.Dir(out), 0o700); err != nil {
		t.Fatal(err)
	}
	made := exec.Command("go", "build", "-C", module, "-o", out, pkg)
	made.Env = append(os.Environ(), "GOCACHEPROG=")
	if rendered, err := made.CombinedOutput(); err != nil {
		t.Fatalf("go build -C %s -o %s %s: %v\n%s\nthe CLI embeds a node bundle: `pnpm install --frozen-lockfile && pnpm --filter ocel build && go generate ./...` in cli/ builds it",
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
		"OCEL_PROVIDERS_DIR="+j.providers,
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

var writingActions = map[string]bool{
	"ACTION_CREATE": true, "ACTION_UPDATE": true, "ACTION_REPLACE": true, "ACTION_DELETE": true, "ACTION_DISABLE_THEN_DELETE": true,
}

type plannedEvent struct {
	Operation struct {
		Phase string `json:"phase"`
		Plan  *struct {
			Groups []struct {
				Name    string `json:"name"`
				Action  string `json:"action"`
				Changes []struct {
					Name   string `json:"name"`
					Action string `json:"action"`
				} `json:"changes"`
			} `json:"groups"`
		} `json:"plan"`
	} `json:"operation"`
}

func plannedWrites(t *testing.T, stream string) []string {
	t.Helper()
	planned := false
	var writes []string
	for line := range strings.Lines(stream) {
		var ev plannedEvent
		if json.Unmarshal([]byte(line), &ev) != nil {
			continue
		}
		planned = planned || ev.Operation.Phase == "PHASE_PLAN"
		if ev.Operation.Plan == nil {
			continue
		}
		for _, group := range ev.Operation.Plan.Groups {
			acting := false
			for _, change := range group.Changes {
				if change.Action != "ACTION_KEEP" {
					acting = true
				}
				if writingActions[change.Action] {
					writes = append(writes, group.Name+"/"+change.Name+" "+change.Action)
				}
			}
			if !acting && writingActions[group.Action] {
				writes = append(writes, group.Name+" "+group.Action)
			}
		}
	}
	if !planned {
		t.Fatalf("the run streamed no plan phase, so nothing here reads what it would write:\n%s", stream)
	}
	return writes
}

func TestAPlanStreamWritesOnlyWhereAGroupOrOneOfItsChangesWrites(t *testing.T) {
	kept := strings.Join([]string{
		`INFO  [plan] a line a human reads`,
		`{"operation":{"time":"2026-09-27T10:00:01Z","level":"LEVEL_INFO","phase":"PHASE_PLAN","subject":"","message":"","started":{}}}`,
		`{"operation":{"time":"2026-09-27T10:00:02Z","level":"LEVEL_INFO","phase":"PHASE_PLAN","subject":"","message":"","plan":{"subject":"production","groups":[{"kind":"stack","name":"aws/ocel-bootstrap","action":"ACTION_KEEP"},{"kind":"stack","name":"aws/ocel-bootstrap-isr","action":"ACTION_KEEP","changes":[{"name":"OcelDispatchFunction","action":"ACTION_KEEP"},{"name":"OcelOriginSecret","action":"ACTION_ADOPT"}]},{"kind":"edge","name":"cloudfront/edge","action":"ACTION_ADOPT"}]}}}`,
	}, "\n")
	if writes := plannedWrites(t, kept); len(writes) > 0 {
		t.Errorf("a plan that keeps and adopts reads as writing %v", writes)
	}

	mixed := `{"operation":{"time":"2026-09-27T10:00:02Z","level":"LEVEL_INFO","phase":"PHASE_PLAN","subject":"","message":"","plan":{"subject":"production","groups":[{"kind":"stack","name":"aws/ocel-bootstrap-isr","action":"ACTION_KEEP"},{"kind":"stack","name":"aws/ocel-bootstrap","action":"ACTION_UPDATE","changes":[{"name":"OcelDispatchFunction","action":"ACTION_UPDATE"},{"name":"OcelOriginSecret","action":"ACTION_KEEP"}]},{"kind":"edge","name":"cloudflare/edge","action":"ACTION_CREATE"}]}}}`
	want := []string{"aws/ocel-bootstrap/OcelDispatchFunction ACTION_UPDATE", "cloudflare/edge ACTION_CREATE"}
	if writes := plannedWrites(t, mixed); !slices.Equal(writes, want) {
		t.Errorf("a plan that updates one change and creates one group reads as writing %v, want %v", writes, want)
	}
}

var escapes = regexp.MustCompile("\x1b\\[[\x30-\x3f]*[\x20-\x2f]*[\x40-\x7e]|\x1b\\][^\x07\x1b]*(\x07|\x1b\\\\)|\x1b[()][0-9A-B]|\x1b[=>]")

func plain(rendered string) string {
	return strings.ReplaceAll(escapes.ReplaceAllString(rendered, ""), "\r\n", "\n")
}

func TestLifecycleTheWholeBootstrapRunsOnTheRealBinaryAndGivesTheAccountBack(t *testing.T) {
	run := lifecycle(t)
	tier := environment.TierProduction
	run.account.emptied(t, tier)
	ctx := context.Background()

	fresh := run.must(t, "doctor")
	if !strings.Contains(fresh, "not set up — run `ocel bootstrap production`") {
		t.Fatalf("`ocel doctor` on an account nothing has written to said:\n%s", fresh)
	}

	applied := run.must(t, "bootstrap", "production", "--yes")
	if !strings.Contains(applied, "Bootstrapped") {
		t.Errorf("`ocel bootstrap production --yes` finished without saying it bootstrapped:\n%s", applied)
	}
	if status := run.account.stackStatus(t, coreStackName); status != "CREATE_COMPLETE" {
		t.Fatalf("%s is in state %q after the CLI applied it, want CREATE_COMPLETE", coreStackName, status)
	}
	deployed, err := bootstrap.CheckDeployedFor(ctx, cloudformation.NewFromConfig(run.account.aws), defaultNamespace, tier)
	if err != nil {
		t.Fatal(err)
	}
	for _, bucket := range []string{deployed.StateBucket, deployed.ArtifactBucket, deployed.AssetBucket} {
		if !run.account.bucketExists(t, bucket) {
			t.Errorf("%s is named by the stack the CLI applied but no bucket answers for it", bucket)
		}
	}
	if !run.account.paramExists(t, passphraseParam) {
		t.Errorf("%s is missing after the CLI bootstrapped, and every Pulumi stack this account deploys is encrypted under it", passphraseParam)
	}

	diagnosis := run.must(t, "doctor")
	if !strings.Contains(diagnosis, "bootstrapped, current") {
		t.Fatalf("`ocel doctor` after an apply still calls production unbootstrapped:\n%s", diagnosis)
	}

	replanned := run.must(t, "bootstrap", "production", "--dry", "--log-format", "json")
	if writes := plannedWrites(t, replanned); len(writes) > 0 {
		t.Errorf("a re-plan over a bootstrapped account would write %v:\n%s", writes, replanned)
	}
	destroyed := run.must(t, "bootstrap", "destroy", "production", "--yes")
	for _, unrecoverable := range []string{"StateBucket", "VariablesTable"} {
		if !strings.Contains(destroyed, unrecoverable) {
			t.Errorf("`ocel bootstrap destroy production` never named %s, and a user confirms without knowing what is unrecoverable:\n%s", unrecoverable, destroyed)
		}
	}

	gone := run.must(t, "doctor")
	if !strings.Contains(gone, "not set up — run `ocel bootstrap production`") {
		t.Errorf("`ocel doctor` after a destroy still claims a bootstrap:\n%s", gone)
	}
	if status := run.account.stackStatus(t, coreStackName); status != "" && status != "DELETE_COMPLETE" {
		t.Errorf("%s is in state %q after a destroy, so the account was not given back", coreStackName, status)
	}
	for _, bucket := range []string{deployed.StateBucket, deployed.ArtifactBucket, deployed.AssetBucket} {
		if run.account.bucketExists(t, bucket) {
			t.Errorf("%s still answers after a destroy, so the account was not given back", bucket)
		}
	}
	if run.account.paramExists(t, passphraseParam) {
		t.Errorf("%s still exists after the last tier on this account went", passphraseParam)
	}
}
