package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/cli/internal/declare"
	"github.com/ocelhq/ocel/cli/internal/manifestbuilder"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
)

func TestConfirmDeploy(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  bool
	}{
		{"lowercase y", "y\n", true},
		{"word yes", "yes\n", true},
		{"uppercase YES", "YES\n", true},
		{"explicit no", "n\n", false},
		{"empty answer defaults to no", "\n", false},
		{"unrecognized answer defaults to no", "sure\n", false},
		{"no input at all defaults to no", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout bytes.Buffer
			got, err := confirmDeploy("my-app", "@ocel/provider-aws", nil, &stdout, strings.NewReader(tc.input))
			if err != nil {
				t.Fatalf("confirmDeploy() error = %v", err)
			}
			if got != tc.want {
				t.Errorf("confirmDeploy(%q) = %v, want %v", tc.input, got, tc.want)
			}
			if !strings.Contains(stdout.String(), "Deploy my-app with @ocel/provider-aws? [y/N]") {
				t.Errorf("stdout = %q, want it to contain the confirm prompt", stdout.String())
			}
		})
	}
}

func TestConfirmDeploy_UnrecognizedSlugWarns(t *testing.T) {
	var stdout bytes.Buffer
	got, err := confirmDeploy("my-app", "@ocel/provider-aws", []string{"my-application", "billing"}, &stdout, strings.NewReader("y\n"))
	if err != nil {
		t.Fatalf("confirmDeploy() error = %v", err)
	}
	if !got {
		t.Error("confirmDeploy() = false, want the y answer to still proceed — the guard is a warning, not a refusal")
	}

	out := stdout.String()
	for _, want := range []string{
		`No existing deployment for slug "my-app".`,
		"This will create a NEW project.",
		"This backend already has: my-application, billing",
		"Continue? [y/N]",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Deploy my-app with") {
		t.Errorf("stdout = %q, want the routine update prompt replaced, not appended to", out)
	}
}

func TestConfirmDeploy_UnrecognizedSlugDefaultsToNo(t *testing.T) {
	for _, input := range []string{"\n", "", "sure\n"} {
		var stdout bytes.Buffer
		got, err := confirmDeploy("my-app", "@ocel/provider-aws", []string{"my-application"}, &stdout, strings.NewReader(input))
		if err != nil {
			t.Fatalf("confirmDeploy(%q) error = %v", input, err)
		}
		if got {
			t.Errorf("confirmDeploy(%q) = true, want the drift prompt to default to No", input)
		}
	}
}

func TestConfirmDeploy_EmptyBackendIsNotNagged(t *testing.T) {
	for _, known := range [][]string{nil, {}} {
		var stdout bytes.Buffer
		if _, err := confirmDeploy("my-app", "@ocel/provider-aws", known, &stdout, strings.NewReader("y\n")); err != nil {
			t.Fatalf("confirmDeploy() error = %v", err)
		}
		out := stdout.String()
		if !strings.Contains(out, "Deploy my-app with @ocel/provider-aws? [y/N]") {
			t.Errorf("stdout = %q, want the routine prompt", out)
		}
		if strings.Contains(out, "NEW project") {
			t.Errorf("stdout = %q, want no drift warning when the backend reports nothing", out)
		}
	}
}

func TestToDeclarations_MapsResourceFields(t *testing.T) {
	resources := []declare.Resource{
		{
			Name:     "main",
			Type:     resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES,
			Postgres: &resourcesv1.PostgresConfig{Version: "17"},
		},
	}

	decls := toDeclarations(resources)

	if len(decls) != 1 {
		t.Fatalf("len(decls) = %d, want 1", len(decls))
	}
	d := decls[0]
	if d.ID != "main" {
		t.Errorf("ID = %q, want %q", d.ID, "main")
	}
	if d.Type != resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES {
		t.Errorf("Type = %v, want RESOURCE_TYPE_POSTGRES", d.Type)
	}
	if d.Postgres.GetVersion() != "17" {
		t.Errorf("Postgres.Version = %q, want %q", d.Postgres.GetVersion(), "17")
	}
}

func TestRunDeploy_InvalidTag_ErrorsBeforeAnything(t *testing.T) {
	var stderr bytes.Buffer
	err := runDeploy(context.Background(), t.TempDir(), deployOptions{tag: "feature/x"}, &bytes.Buffer{}, &stderr, strings.NewReader(""))
	if err == nil {
		t.Fatal("runDeploy err = nil, want an error for an invalid tag")
	}
	if !strings.Contains(err.Error(), "invalid character") {
		t.Errorf("err = %v, want it to explain the invalid character", err)
	}
}

func TestRunDeploy_MissingConfig_ErrorsBeforeAnySpawn(t *testing.T) {
	err := runDeploy(context.Background(), t.TempDir(), deployOptions{yes: true}, &bytes.Buffer{}, &bytes.Buffer{}, strings.NewReader(""))
	if err == nil {
		t.Fatal("runDeploy err = nil, want error")
	}
	if !strings.Contains(err.Error(), "ocel init") {
		t.Fatalf("err = %v, want it to hint at `ocel init`", err)
	}
}

func TestRunDeploy_MalformedConfig_ErrorsBeforeAnySpawn(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "ocel.config.ts"), `this is not valid TypeScript {{{`)

	err := runDeploy(context.Background(), root, deployOptions{yes: true}, &bytes.Buffer{}, &bytes.Buffer{}, strings.NewReader(""))
	if err == nil {
		t.Fatal("runDeploy err = nil, want error")
	}
	if !strings.Contains(err.Error(), "ocel.config.ts") {
		t.Fatalf("err = %v, want it to mention ocel.config.ts", err)
	}
}

func TestRunDeploy_NoProviderConfigured_ErrorsBeforeAnySpawn(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
};
`)

	err := runDeploy(context.Background(), root, deployOptions{yes: true}, &bytes.Buffer{}, &bytes.Buffer{}, strings.NewReader(""))
	if err == nil {
		t.Fatal("runDeploy err = nil, want error")
	}
	if !strings.Contains(err.Error(), "provider") {
		t.Fatalf("err = %v, want it to mention the missing provider", err)
	}
}

func TestRunDeploy_HappyPath_DiscoversBuildsSpawnsAndDeploysToSuccess(t *testing.T) {
	root, sockPath := setUpDeployFixture(t)

	var stdout, stderr bytes.Buffer
	err := runDeploy(context.Background(), root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
	if err != nil {
		t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	if !strings.Contains(stdout.String(), "provisioning...") {
		t.Errorf("stdout = %q, want it to contain the streamed progress event", stdout.String())
	}
	if !strings.Contains(stdout.String(), "Deployed") {
		t.Errorf("stdout = %q, want a terminal success message", stdout.String())
	}
	if !strings.Contains(stdout.String(), "DEPLOY class=CLASS_PRODUCTION lifecycle=LIFECYCLE_UNSPECIFIED") {
		t.Errorf("stdout = %q, want deploy to send a production Environment", stdout.String())
	}
	if strings.Contains(stdout.String(), "[y/N]") {
		t.Errorf("stdout = %q, want the confirm prompt skipped by --yes", stdout.String())
	}

	waitForNoStaleSocket(t, sockPath)
}

func TestRunDeploy_WithApp_BuildsFunctionsIntoManifest(t *testing.T) {
	root, sockPath := setUpDeployFixture(t)
	addAppToFixtureConfig(t, root)
	stubAppFunctions(t, []manifestbuilder.Function{
		{
			Name:         "api",
			Runtime:      "nodejs24.x",
			Handler:      "src/server.js",
			ArtifactPath: "output/api",
			Framework:    "express",
			App:          "api",
		},
	})

	var stdout, stderr bytes.Buffer
	err := runDeploy(context.Background(), root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
	if err != nil {
		t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	out := stdout.String()
	if !strings.Contains(out, "FUNCTION logical_name=api runtime=nodejs24.x handler=src/server.js artifact_path=output/api framework=express app=api") {
		t.Errorf("stdout = %q, want the function to have reached the manifest", out)
	}
	if strings.Contains(stderr.String(), "deploying infrastructure only") {
		t.Errorf("stderr = %q, want no infra-only warning when a function is built", stderr.String())
	}
	if !strings.Contains(out, "Deployed") {
		t.Errorf("stdout = %q, want a terminal success message", out)
	}

	waitForNoStaleSocket(t, sockPath)
}

func TestRunDeploy_NoApps_WarnsAndDeploysResourcesOnly(t *testing.T) {
	root, sockPath := setUpDeployFixture(t)

	var stdout, stderr bytes.Buffer
	err := runDeploy(context.Background(), root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
	if err != nil {
		t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	if !strings.Contains(stdout.String(), "no functions to deploy; deploying infrastructure only") {
		t.Errorf("stdout = %q, want the infra-only warning", stdout.String())
	}
	if !strings.Contains(stdout.String(), "Deployed") {
		t.Errorf("stdout = %q, want resources to still deploy to success", stdout.String())
	}
	if strings.Contains(stdout.String(), "FUNCTION ") {
		t.Errorf("stdout = %q, want no function echoed when no apps are configured", stdout.String())
	}
	if strings.Contains(stdout.String(), "APP ") {
		t.Errorf("stdout = %q, want no app echoed when nothing was built", stdout.String())
	}

	waitForNoStaleSocket(t, sockPath)
}

func TestRunDeploy_AppBuildFailure_AbortsBeforeSpawn(t *testing.T) {
	root, _ := setUpDeployFixture(t)
	addAppToFixtureConfig(t, root)
	prev := buildApp
	buildApp = func(context.Context, *projectconfig.Config, map[string]map[string]string, io.Writer) error {
		return errors.New("boom: app build failed")
	}
	t.Cleanup(func() { buildApp = prev })

	var stdout, stderr bytes.Buffer
	err := runDeploy(context.Background(), root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
	if err == nil {
		t.Fatal("runDeploy err = nil, want the app-build failure")
	}
	if !strings.Contains(stdout.String(), "boom: app build failed") {
		t.Errorf("stdout = %q, want the app-build failure surfaced", stdout.String())
	}
	if strings.Contains(stdout.String(), "DEPLOY ") {
		t.Errorf("stdout = %q, want no Deploy to have been driven", stdout.String())
	}
}

func TestRunDeploy_RefusesOnClassMismatch_NoDeploy(t *testing.T) {
	root, _ := setUpDeployFixture(t)
	t.Setenv(fakeInfraClassEnvVar, "preview")
	t.Setenv(fakeInfraPresentEnvVar, "1")

	var stdout, stderr bytes.Buffer
	err := runDeploy(context.Background(), root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
	if err == nil {
		t.Fatal("runDeploy err = nil, want a class-mismatch error")
	}
	if !strings.Contains(stdout.String(), "ocel deploy can only run against production infrastructure") {
		t.Errorf("stdout = %q, want the concrete class-mismatch message", stdout.String())
	}
	if strings.Contains(stdout.String(), "DEPLOY ") {
		t.Errorf("stdout = %q, want no Deploy to have been driven", stdout.String())
	}
}

func TestRunDeploy_RefusesWhenInfraAbsent_NoDeploy(t *testing.T) {
	root, _ := setUpDeployFixture(t)
	t.Setenv(fakeInfraPresentEnvVar, "0")

	var stdout, stderr bytes.Buffer
	err := runDeploy(context.Background(), root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
	if err == nil {
		t.Fatal("runDeploy err = nil, want a missing-infrastructure error")
	}
	if !strings.Contains(stdout.String(), "ocel bootstrap") {
		t.Errorf("stdout = %q, want it to direct the user to `ocel bootstrap`", stdout.String())
	}
	if strings.Contains(stdout.String(), "DEPLOY ") {
		t.Errorf("stdout = %q, want no Deploy to have been driven", stdout.String())
	}
}

func TestRunDeploy_ConfirmSkippedWhenStdinNotATTY_ProceedsWithoutPrompting(t *testing.T) {
	root, sockPath := setUpDeployFixture(t)

	var stdout, stderr bytes.Buffer
	err := runDeploy(context.Background(), root, deployOptions{yes: false}, &stdout, &stderr, strings.NewReader(""))
	if err != nil {
		t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	if strings.Contains(stdout.String(), "[y/N]") {
		t.Errorf("stdout = %q, want the confirm prompt skipped for non-TTY stdin", stdout.String())
	}
	if !strings.Contains(stdout.String(), "Deployed") {
		t.Errorf("stdout = %q, want deploy to still proceed to success", stdout.String())
	}

	waitForNoStaleSocket(t, sockPath)
}

func TestRunDeploy_PreflightCarriesTheSlug(t *testing.T) {
	root, sockPath := setUpDeployFixture(t)

	var stdout, stderr bytes.Buffer
	if err := runDeploy(context.Background(), root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "PREFLIGHT slug=test-app") {
		t.Errorf("stdout = %q, want the preflight to have carried the project's slug", stdout.String())
	}

	waitForNoStaleSocket(t, sockPath)
}

func TestRunDeploy_YesBypassesTheSlugDriftGuard(t *testing.T) {
	root, sockPath := setUpDeployFixture(t)
	t.Setenv(fakeKnownSlugsEnvVar, "my-application,billing")

	var stdout, stderr bytes.Buffer
	if err := runDeploy(context.Background(), root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	out := stdout.String()
	if strings.Contains(out, "NEW project") || strings.Contains(out, "[y/N]") {
		t.Errorf("stdout = %q, want --yes to bypass the drift prompt", out)
	}
	if !strings.Contains(out, "Deployed") {
		t.Errorf("stdout = %q, want the deploy to proceed", out)
	}

	waitForNoStaleSocket(t, sockPath)
}

func pretendStdoutIsTerminal(t *testing.T) {
	t.Helper()
	prior := stdoutIsTerminal
	stdoutIsTerminal = func(io.Writer) bool { return true }
	t.Cleanup(func() { stdoutIsTerminal = prior })
}

func TestRunDeploy_PrintsIdentityBanner_BeforeBuildAndDeploy(t *testing.T) {
	pretendStdoutIsTerminal(t)
	root, sockPath := setUpDeployFixture(t)
	t.Setenv(fakeIDAwsAccountEnvVar, "123456789012")
	t.Setenv(fakeIDAwsProfileEnvVar, "default")
	t.Setenv(fakeIDAwsRegionEnvVar, "us-east-1")
	t.Setenv(fakeIDCfAccountEnvVar, "abcd1234")

	var stdout, stderr bytes.Buffer
	if err := runDeploy(context.Background(), root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	out := stdout.String()
	for _, want := range []string{"Running with:", "profile=default", "account=123456789012", "region=us-east-1", "Cloudflare  account=abcd1234"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout missing %q:\n%s", want, out)
		}
	}
	banner := strings.Index(out, "Running with:")
	build := strings.Index(out, "Building project")
	deploy := strings.Index(out, "DEPLOY ")
	if banner < 0 || build < 0 || deploy < 0 {
		t.Fatalf("expected banner, build, and deploy all present; banner=%d build=%d deploy=%d\n%s", banner, build, deploy, out)
	}
	if !(banner < build && build < deploy) {
		t.Errorf("expected order banner < build < deploy; got banner=%d build=%d deploy=%d\n%s", banner, build, deploy, out)
	}

	waitForNoStaleSocket(t, sockPath)
}

func TestRunDeploy_WithoutATerminal_OmitsIdentityBanner(t *testing.T) {
	root, sockPath := setUpDeployFixture(t)
	t.Setenv(fakeIDAwsAccountEnvVar, "123456789012")
	t.Setenv(fakeIDAwsProfileEnvVar, "default")
	t.Setenv(fakeIDAwsRegionEnvVar, "us-east-1")
	t.Setenv(fakeIDCfAccountEnvVar, "abcd1234")

	var stdout, stderr bytes.Buffer
	if err := runDeploy(context.Background(), root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	out := stdout.String()
	if !strings.Contains(out, "Deployed") {
		t.Fatalf("stdout = %q, want the deploy to have proceeded", out)
	}
	for _, leaked := range []string{"Running with:", "123456789012", "abcd1234", "profile=default"} {
		if strings.Contains(out+stderr.String(), leaked) {
			t.Errorf("output leaked %q with no terminal to print it to:\n%s", leaked, out)
		}
	}

	waitForNoStaleSocket(t, sockPath)
}

func TestRunDeploy_CredentialProblem_AbortsBeforeBuildAndDeploy(t *testing.T) {
	pretendStdoutIsTerminal(t)
	root, _ := setUpDeployFixture(t)
	t.Setenv(fakeIDAwsAccountEnvVar, "123456789012")
	t.Setenv(fakeIDAwsProfileEnvVar, "default")
	t.Setenv(fakeCredProblemEnvVar, "Cloudflare")

	var stdout, stderr bytes.Buffer
	err := runDeploy(context.Background(), root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
	if err == nil {
		t.Fatal("runDeploy err = nil, want a credential-check error")
	}

	out := stdout.String()
	if !strings.Contains(out, "account=123456789012") {
		t.Errorf("stdout = %q, want the resolved AWS identity still shown", out)
	}
	if !strings.Contains(out, "Cloudflare") {
		t.Errorf("stdout = %q, want the Cloudflare credential problem surfaced", out)
	}
	if strings.Contains(out, "Building project") {
		t.Errorf("stdout = %q, want the build to be skipped on a credential failure", out)
	}
	if strings.Contains(out, "DEPLOY ") {
		t.Errorf("stdout = %q, want no Deploy to have been driven", out)
	}
}

func setUpDeployFixture(t *testing.T) (root, sockPath string) {
	t.Helper()

	if runtime.GOOS == "windows" {
		t.Skip("uses a Unix-domain-socket fake provider and POSIX symlinks")
	}
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not found on PATH")
	}

	setLoggedIn(t)

	prevTimeout := deployReadyTimeout
	deployReadyTimeout = 5 * time.Second
	t.Cleanup(func() { deployReadyTimeout = prevTimeout })

	root = t.TempDir()
	writeFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  provider: { package: "@ocel/provider-aws", options: {} },
  domains: { preview: "*.preview.acme.com" },
};
`)
	writeFile(t, filepath.Join(root, "ocel", "main.ts"), `
declare global {
  var __ocelRegister: Promise<unknown>[];
}
globalThis.__ocelRegister ??= [];
globalThis.__ocelRegister.push(
  fetch(new URL("/resources.v1.ResourceService/Declare", process.env.OCEL_DEV_SERVER), {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      resource: { type: "RESOURCE_TYPE_POSTGRES", name: "main" },
      postgres: { version: "17" },
    }),
  }),
);
export {};
`)

	binDir := filepath.Join(root, "node_modules", "@ocel", "provider-aws-"+nodePlatformSuffix(t), "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", binDir, err)
	}
	testBinary, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatalf("resolve test binary path: %v", err)
	}
	if err := os.Symlink(testBinary, filepath.Join(binDir, "deploy")); err != nil {
		t.Fatalf("symlink fake provider binary: %v", err)
	}

	sockPath = filepath.Join(t.TempDir(), "deploy-provider.sock")
	t.Setenv(deployFakeProviderEnvVar, "1")
	t.Setenv(deployFakeProviderSockEnvVar, sockPath)

	t.Setenv(fakeInfraClassEnvVar, "production")
	t.Setenv(fakeInfraPresentEnvVar, "1")

	stubAppFunctions(t, nil)

	return root, sockPath
}

func addAppToFixtureConfig(t *testing.T, root string) {
	t.Helper()
	writeFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  provider: { package: "@ocel/provider-aws", options: {} },
  domains: { preview: "*.preview.acme.com" },
  apps: [{ name: "api", path: "apps/api", framework: "express" }],
};
`)
}

func stubAppFunctions(t *testing.T, functions []manifestbuilder.Function) {
	t.Helper()
	prevBuild, prevCollect := buildApp, collectAppFunctions
	buildApp = func(context.Context, *projectconfig.Config, map[string]map[string]string, io.Writer) error {
		return nil
	}
	collectAppFunctions = func(string) ([]manifestbuilder.Function, error) {
		return functions, nil
	}
	t.Cleanup(func() { buildApp, collectAppFunctions = prevBuild, prevCollect })
}

func nodePlatformSuffix(t *testing.T) string {
	t.Helper()

	nodePlatform := map[string]string{"darwin": "darwin", "linux": "linux"}[runtime.GOOS]
	if nodePlatform == "" {
		t.Skipf("no node platform mapping for GOOS=%s", runtime.GOOS)
	}
	nodeArch := map[string]string{"amd64": "x64", "arm64": "arm64"}[runtime.GOARCH]
	if nodeArch == "" {
		t.Skipf("no node arch mapping for GOARCH=%s", runtime.GOARCH)
	}
	return nodePlatform + "-" + nodeArch
}

func waitForNoStaleSocket(t *testing.T, sockPath string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(sockPath); os.IsNotExist(err) {
			return
		}
		if time.Now().After(deadline) {
			t.Errorf("stale socket file left behind at %s", sockPath)
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestRunDeploy_SingleApp_ProducesExactlyOneAttributedApp(t *testing.T) {
	root, sockPath := setUpDeployFixture(t)
	writeFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  provider: { package: "@ocel/provider-aws", options: {} },
  apps: [{ name: "api", path: "apps/api", framework: "express", domains: { production: "Api.Acme.com" } }],
};
`)
	stubAppFunctions(t, []manifestbuilder.Function{
		{Name: "api", Runtime: "nodejs24.x", Handler: "src/server.js", ArtifactPath: "output/api", Framework: "express", App: "api"},
	})

	var stdout, stderr bytes.Buffer
	if err := runDeploy(context.Background(), root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	out := stdout.String()
	if got := strings.Count(out, "APP "); got != 1 {
		t.Fatalf("stdout echoed %d apps, want exactly 1:\n%s", got, out)
	}
	if !strings.Contains(out, "APP name=api framework=express production_domain=api.acme.com") {
		t.Errorf("stdout = %q, want the app with its per-app production domain", out)
	}
	if !strings.Contains(out, "framework=express app=api") {
		t.Errorf("stdout = %q, want the function attributed to the api app", out)
	}

	waitForNoStaleSocket(t, sockPath)
}

func TestRunDeploy_TwoApps_AttributesFunctionsToTheirApps(t *testing.T) {
	root, sockPath := setUpDeployFixture(t)
	writeFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  provider: { package: "@ocel/provider-aws", options: {} },
  apps: [
    { name: "web", path: "apps/web", framework: "express", domains: { production: "acme.com" } },
    { name: "admin", path: "apps/admin", framework: "express" },
  ],
};
`)
	stubAppFunctions(t, []manifestbuilder.Function{
		{Name: "web", Runtime: "nodejs24.x", Handler: "src/server.js", ArtifactPath: "output/web", Framework: "express", App: "web"},
		{Name: "admin", Runtime: "nodejs24.x", Handler: "src/server.js", ArtifactPath: "output/admin", Framework: "express", App: "admin"},
	})

	var stdout, stderr bytes.Buffer
	if err := runDeploy(context.Background(), root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	out := stdout.String()
	if got := strings.Count(out, "APP "); got != 2 {
		t.Fatalf("stdout echoed %d apps, want exactly 2:\n%s", got, out)
	}
	if !strings.Contains(out, "APP name=admin framework=express production_domain=") {
		t.Errorf("stdout = %q, want the admin app with no domain of its own", out)
	}
	if !strings.Contains(out, "APP name=web framework=express production_domain=acme.com") {
		t.Errorf("stdout = %q, want the web app with its own production domain", out)
	}
	if !strings.Contains(out, "logical_name=web") || !strings.Contains(out, "artifact_path=output/web framework=express app=web") {
		t.Errorf("stdout = %q, want the web function attributed to the web app", out)
	}
	if !strings.Contains(out, "logical_name=admin") || !strings.Contains(out, "artifact_path=output/admin framework=express app=admin") {
		t.Errorf("stdout = %q, want the admin function attributed to the admin app", out)
	}

	waitForNoStaleSocket(t, sockPath)
}

func TestRunDeploy_DetectedApp_AppearsInManifest(t *testing.T) {
	root, sockPath := setUpDeployFixture(t)
	stubAppFunctions(t, []manifestbuilder.Function{
		{Name: "index", Runtime: "nodejs24.x", Handler: "h.js", ArtifactPath: "output/index", Framework: "next", App: "express-app"},
	})

	var stdout, stderr bytes.Buffer
	if err := runDeploy(context.Background(), root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	out := stdout.String()
	if got := strings.Count(out, "APP "); got != 1 {
		t.Fatalf("stdout echoed %d apps, want exactly 1:\n%s", got, out)
	}
	if !strings.Contains(out, "APP name=express-app framework=next production_domain=") {
		t.Errorf("stdout = %q, want the detected app named in the manifest", out)
	}

	waitForNoStaleSocket(t, sockPath)
}
