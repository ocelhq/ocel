package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"buf.build/go/protovalidate"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	runnerv1 "github.com/ocelhq/ocel/pkg/proto/runner/v1"
	"github.com/ocelhq/ocel/pkg/runner"
)

const testProject = "00000000-0000-4000-8000-000000000001"

func TestARunReadsItsJobFromTheJobContextFile(t *testing.T) {
	dir := t.TempDir()
	spec := &runnerv1.GetSpecResponse{
		JobId: "job_2", ProjectId: testProject, Tier: environmentv1.Tier_TIER_PREVIEW, Slug: "blog",
		DeploymentId: strings.Repeat("ab", 16), ConsoleUrl: "https://console.example.com",
		Job: &runnerv1.JobRequest{
			Command: runnerv1.JobCommand_JOB_COMMAND_PREVIEW_UP,
			Preview: &runnerv1.PreviewTarget{Name: "feat-x", Label: "pr-7"},
			Git:     &runnerv1.GitContext{Commit: "0123abc", Branch: "main", Dirty: true, PullRequest: 7},
		},
	}
	if err := writeJobContext(dir, spec, filepath.Join(dir, jobTokenFile)); err != nil {
		t.Fatal(err)
	}
	encoded, err := os.ReadFile(filepath.Join(dir, jobContextFile))
	if err != nil {
		t.Fatal(err)
	}
	read := &runnerv1.JobContext{}
	if err := protojson.Unmarshal(encoded, read); err != nil {
		t.Fatalf("the job context %s: %v", encoded, err)
	}
	if err := protovalidate.Validate(read); err != nil {
		t.Fatalf("the job context %s is not valid: %v", encoded, err)
	}
	want := &runnerv1.JobContext{
		JobId: "job_2", ProjectId: testProject, Tier: environmentv1.Tier_TIER_PREVIEW, DeploymentId: strings.Repeat("ab", 16),
		Slug: "blog", Git: spec.GetJob().GetGit(), PreviewLabel: "pr-7", ConsoleUrl: "https://console.example.com",
		JobTokenFile: filepath.Join(dir, jobTokenFile),
	}
	if !proto.Equal(read, want) {
		t.Errorf("the run reads %v, want %v", read, want)
	}
}

func TestAJobsArgsAreItsCommandWithJSONAndNeverYes(t *testing.T) {
	for _, tt := range []struct {
		job  *runnerv1.JobRequest
		tier environmentv1.Tier
		want []string
	}{
		{&runnerv1.JobRequest{Command: runnerv1.JobCommand_JOB_COMMAND_BUILD}, environmentv1.Tier_TIER_PRODUCTION, []string{"build", "--json"}},
		{&runnerv1.JobRequest{Command: runnerv1.JobCommand_JOB_COMMAND_BUILD, TierValues: true}, environmentv1.Tier_TIER_PRODUCTION, []string{"build", "--env", "production", "--json"}},
		{&runnerv1.JobRequest{Command: runnerv1.JobCommand_JOB_COMMAND_BUILD, TierValues: true}, environmentv1.Tier_TIER_PREVIEW, []string{"build", "--env", "preview", "--json"}},
		{&runnerv1.JobRequest{Command: runnerv1.JobCommand_JOB_COMMAND_DEPLOY}, environmentv1.Tier_TIER_PRODUCTION, []string{"deploy", "--json"}},
		{&runnerv1.JobRequest{Command: runnerv1.JobCommand_JOB_COMMAND_DEPLOY, Tag: "v1.2"}, environmentv1.Tier_TIER_PRODUCTION, []string{"deploy", "--json", "--tag", "v1.2"}},
		{&runnerv1.JobRequest{Command: runnerv1.JobCommand_JOB_COMMAND_PREVIEW_UP, Preview: &runnerv1.PreviewTarget{Name: "feat-x"}}, environmentv1.Tier_TIER_PREVIEW, []string{"preview", "up", "feat-x", "--json"}},
		{&runnerv1.JobRequest{Command: runnerv1.JobCommand_JOB_COMMAND_PREVIEW_UP, Preview: &runnerv1.PreviewTarget{Name: "feat-x", Persistent: true}}, environmentv1.Tier_TIER_PREVIEW, []string{"preview", "up", "feat-x", "--json", "--persistent"}},
		{&runnerv1.JobRequest{Command: runnerv1.JobCommand_JOB_COMMAND_PREVIEW_RM, Preview: &runnerv1.PreviewTarget{Name: "feat-x"}}, environmentv1.Tier_TIER_PREVIEW, []string{"preview", "rm", "feat-x", "--json"}},
		{&runnerv1.JobRequest{Command: runnerv1.JobCommand_JOB_COMMAND_PROJECTS_DESTROY}, environmentv1.Tier_TIER_PREVIEW, []string{"projects", "destroy", "--tier", "preview", "--json"}},
		{&runnerv1.JobRequest{Command: runnerv1.JobCommand_JOB_COMMAND_PROJECTS_DESTROY}, environmentv1.Tier_TIER_PRODUCTION, []string{"projects", "destroy", "--tier", "production", "--json"}},
	} {
		if got := Args(tt.job, tt.tier); !slices.Equal(got, tt.want) {
			t.Errorf("Args(%v, %v) = %q, want %q", tt.job.GetCommand(), tt.tier, got, tt.want)
		}
	}
}

func TestAJobStartedWithAConfigFileRunsItsCommandWithThatConfig(t *testing.T) {
	staging := "ocel.staging.json"
	for _, tt := range []struct {
		job  *runnerv1.JobRequest
		tier environmentv1.Tier
		want []string
	}{
		{&runnerv1.JobRequest{Command: runnerv1.JobCommand_JOB_COMMAND_BUILD, ConfigFile: staging}, environmentv1.Tier_TIER_PRODUCTION, []string{"build", "--json", "--config", staging}},
		{&runnerv1.JobRequest{Command: runnerv1.JobCommand_JOB_COMMAND_DEPLOY, Tag: "v1.2", ConfigFile: staging}, environmentv1.Tier_TIER_PRODUCTION, []string{"deploy", "--json", "--tag", "v1.2", "--config", staging}},
		{&runnerv1.JobRequest{Command: runnerv1.JobCommand_JOB_COMMAND_PREVIEW_UP, Preview: &runnerv1.PreviewTarget{Name: "feat-x"}, ConfigFile: staging}, environmentv1.Tier_TIER_PREVIEW, []string{"preview", "up", "feat-x", "--json", "--config", staging}},
		{&runnerv1.JobRequest{Command: runnerv1.JobCommand_JOB_COMMAND_PREVIEW_RM, Preview: &runnerv1.PreviewTarget{Name: "feat-x"}, ConfigFile: staging}, environmentv1.Tier_TIER_PREVIEW, []string{"preview", "rm", "feat-x", "--json", "--config", staging}},
		{&runnerv1.JobRequest{Command: runnerv1.JobCommand_JOB_COMMAND_PROJECTS_DESTROY, ConfigFile: staging}, environmentv1.Tier_TIER_PREVIEW, []string{"projects", "destroy", "--tier", "preview", "--json", "--config", staging}},
	} {
		if got := Args(tt.job, tt.tier); !slices.Equal(got, tt.want) {
			t.Errorf("Args(%v, %v) = %q, want %q", tt.job.GetCommand(), tt.tier, got, tt.want)
		}
	}
}

func TestBuildIDsAreReadOnlyFromWithinTheSource(t *testing.T) {
	dir := t.TempDir()
	source, outside := filepath.Join(dir, "source"), filepath.Join(dir, "outside")
	id := strings.Repeat("a", 32)
	for _, project := range []string{outside, filepath.Join(source, "real"), source} {
		write(t, filepath.Join(buildoutput.AppRoot(filepath.Join(project, filepath.FromSlash(buildoutput.Dir)), "web"), buildIDFile), id)
	}
	if builds := buildIDs(source, "real"); len(builds) != 1 || builds[0].GetApp() != "web" || builds[0].GetBuildId() != id {
		t.Fatalf("read %v from the project in the source, want web's build", builds)
	}
	if builds := buildIDs(source, ""); len(builds) != 1 {
		t.Fatalf("read %v from the project at the source's root, want web's build", builds)
	}
	if err := os.Symlink(outside, filepath.Join(source, "app")); err != nil {
		t.Fatal(err)
	}
	if builds := buildIDs(source, "app"); builds != nil {
		t.Fatalf("read %v through a symlink out of the source", builds)
	}
}

func TestACloneFetchesTheOneCommitWithTheTokenKeptOutOfItsArguments(t *testing.T) {
	bin := t.TempDir()
	log := filepath.Join(t.TempDir(), "calls")
	commit := strings.Repeat("c", 40)
	write(t, filepath.Join(bin, "git"), "#!/bin/sh\necho \"$*|$GIT_CONFIG_KEY_0|$GIT_CONFIG_VALUE_0|$GIT_ALLOW_PROTOCOL\" >> \""+log+"\"\n"+
		"case \"$*\" in *rev-parse*) echo "+commit+" ;; esac\n")
	if err := os.Chmod(filepath.Join(bin, "git"), 0o755); err != nil {
		t.Fatal(err)
	}
	source := &runnerv1.GitSource{CloneUrl: "https://git.example.com/acme/web.git", Commit: commit, Token: "the-installation-token"}
	if err := clone(context.Background(), filepath.Join(bin, "git"), filepath.Join(t.TempDir(), "source"), t.TempDir(), source); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	calls := strings.Split(strings.TrimSpace(string(raw)), "\n")
	fetch := slices.IndexFunc(calls, func(call string) bool { return strings.HasPrefix(call, "fetch ") })
	if fetch < 0 {
		t.Fatalf("git was called %q, with no fetch", calls)
	}
	args, header, _ := strings.Cut(calls[fetch], "|")
	if strings.Contains(args, "the-installation-token") {
		t.Errorf("git fetch had the token in its arguments: %q", args)
	}
	if args != "fetch --quiet --depth 1 --no-tags https://git.example.com/acme/web.git "+commit {
		t.Errorf("git %s, want the one commit fetched", args)
	}
	if want := "http.extraHeader|Authorization: Basic eC1hY2Nlc3MtdG9rZW46dGhlLWluc3RhbGxhdGlvbi10b2tlbg==|https"; header != want {
		t.Errorf("git fetch ran with config %q, want %q", header, want)
	}
}

func TestACloneThatChecksOutAnotherCommitIsRefused(t *testing.T) {
	bin := t.TempDir()
	write(t, filepath.Join(bin, "git"), "#!/bin/sh\ncase \"$*\" in *rev-parse*) echo "+strings.Repeat("d", 40)+" ;; esac\n")
	if err := os.Chmod(filepath.Join(bin, "git"), 0o755); err != nil {
		t.Fatal(err)
	}
	source := &runnerv1.GitSource{CloneUrl: "https://git.example.com/acme/web.git", Commit: strings.Repeat("c", 40), Token: "t"}
	if err := clone(context.Background(), filepath.Join(bin, "git"), filepath.Join(t.TempDir(), "source"), t.TempDir(), source); err == nil || !strings.Contains(err.Error(), "not "+strings.Repeat("c", 40)) {
		t.Fatalf("clone() = %v, want the other commit refused", err)
	}
}

type fakeRunner struct {
	ran string

	mu       sync.Mutex
	cancel   bool
	reports  []report
	finished *runnerv1.FinishRequest
}

type report struct {
	lines, stderr []string
	ocelRan       bool
}

func (f *fakeRunner) GetSpec(context.Context, *runnerv1.GetSpecRequest) (*runnerv1.GetSpecResponse, error) {
	panic("the test hands the agent its spec")
}

func (f *fakeRunner) AppendOutput(_ context.Context, request *runnerv1.AppendOutputRequest) (*runnerv1.AppendOutputResponse, error) {
	_, err := os.Stat(f.ran)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reports = append(f.reports, report{lines: request.GetLines(), stderr: request.GetStderr(), ocelRan: err == nil})
	return &runnerv1.AppendOutputResponse{Cancel: f.cancel}, nil
}

func (f *fakeRunner) AwaitAnswer(ctx context.Context, _ *runnerv1.AwaitAnswerRequest) (*runnerv1.AwaitAnswerResponse, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func (f *fakeRunner) Finish(_ context.Context, request *runnerv1.FinishRequest) (*runnerv1.FinishResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.finished = request
	return &runnerv1.FinishResponse{}, nil
}

func (f *fakeRunner) beforeOcel() []report {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.DeleteFunc(slices.Clone(f.reports), func(r report) bool { return r.ocelRan })
}

func (f *fakeRunner) ocelRan() bool {
	_, err := os.Stat(f.ran)
	return err == nil
}

func runJob(t *testing.T, files map[string]string, npm string, configure func(*Config, *fakeRunner)) *fakeRunner {
	t.Helper()
	return runJobAs(t, files, npm, configure, nil)
}

func runJobAs(t *testing.T, files map[string]string, npm string, configure func(*Config, *fakeRunner), started func(*runnerv1.JobRequest)) *fakeRunner {
	t.Helper()
	blobs := map[string][]byte{}
	manifest := &runnerv1.SourceManifest{}
	spec := &runnerv1.GetSpecResponse{
		JobId: "job_1", ProjectId: testProject, Tier: environmentv1.Tier_TIER_PRODUCTION, Slug: "web", ConsoleUrl: "https://console.example.com",
		Job:     &runnerv1.JobRequest{Command: runnerv1.JobCommand_JOB_COMMAND_BUILD, Source: &runnerv1.JobRequest_SourceId{SourceId: strings.Repeat("0", 64)}},
		Archive: &runnerv1.SourceArchive{Manifest: manifest},
	}
	if started != nil {
		started(spec.Job)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(blobs[strings.TrimPrefix(r.URL.Path, "/")])
	}))
	t.Cleanup(server.Close)
	for name, content := range files {
		sum := sha256.Sum256([]byte(content))
		digest := hex.EncodeToString(sum[:])
		blobs[digest] = []byte(content)
		manifest.Files = append(manifest.Files, &runnerv1.SourceFile{Path: name, Digest: digest, Size: uint64(len(content))})
		spec.Archive.Blobs = append(spec.Archive.Blobs, &runnerv1.SourceBlob{Digest: digest, Url: server.URL + "/" + digest})
	}

	bin := t.TempDir()
	write(t, filepath.Join(bin, "npm"), "#!/bin/sh\n"+npm)
	write(t, filepath.Join(bin, "ocel"), "#!/bin/sh\ntouch \"$RAN\"\n")
	for _, name := range []string{"npm", "ocel"} {
		if err := os.Chmod(filepath.Join(bin, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	fake := &fakeRunner{ran: filepath.Join(t.TempDir(), "ran")}
	config := Config{
		Runner: "http://runner.invalid", Token: "the-token", Ocel: filepath.Join(bin, "ocel"), Dir: t.TempDir(),
		Env:         []string{"PATH=" + bin + ":/usr/bin:/bin", "RAN=" + fake.ran},
		ReportEvery: 10 * time.Millisecond, BeatEvery: 40 * time.Millisecond, StopGrace: 10 * time.Second, HTTP: http.DefaultClient,
	}
	config.FindInstalls = npmCI
	if configure != nil {
		configure(&config, fake)
	}
	a := &agent{config: config, client: fake}
	ended := make(chan error, 1)
	go func() { ended <- a.run(context.Background(), spec) }()
	select {
	case err := <-ended:
		if err != nil {
			t.Fatalf("the agent: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the agent never ended")
	}
	return fake
}

var npmProject = map[string]string{"package.json": withDependencies, "package-lock.json": "{}", "index.ts": "export {}\n"}

const withDependencies = `{"dependencies":{"ocel":"^0.0.4"}}`

func npmCI(_ context.Context, _, project string) ([]Install, error) {
	if _, err := os.Stat(filepath.Join(project, "package.json")); err != nil {
		return nil, nil
	}
	return []Install{{Dir: project, Args: []string{"npm", "ci"}}}, nil
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func outputs(lines []string, stream progressv1.Stream) []string {
	var said []string
	for _, line := range lines {
		event := &streamv1.RunEvent{}
		if protojson.Unmarshal([]byte(line), event) == nil && event.GetOperation().GetOutput().GetStream() == stream {
			said = append(said, event.GetOperation().GetMessage())
		}
	}
	return said
}

func TestAnInstallsLinesAndBeatsAreReportedBeforeOcelStarts(t *testing.T) {
	fake := runJob(t, npmProject, `echo '{"summary":{"success":false}}'
echo "npm warn deprecated left-pad" >&2
sleep 0.3
`, nil)
	if !fake.ocelRan() || fake.finished.GetError() != "" || fake.finished.GetExitCode() != 0 {
		t.Fatalf("the job finished %v, ocel run %t; want ocel run once the install succeeded", fake.finished, fake.ocelRan())
	}
	var lines, stderr []string
	beats := 0
	for _, r := range fake.beforeOcel() {
		lines, stderr = append(lines, r.lines...), append(stderr, r.stderr...)
		if len(r.lines)+len(r.stderr) == 0 {
			beats++
		}
	}
	if !slices.ContainsFunc(lines, func(line string) bool {
		return strings.Contains(line, "Installing the dependencies in the project's root with npm ci")
	}) {
		t.Errorf("before ocel started the job reported %q, with no word of the install", lines)
	}
	if said := outputs(lines, progressv1.Stream_STREAM_STDOUT); !slices.Equal(said, []string{`{"summary":{"success":false}}`}) {
		t.Errorf("before ocel started the job reported the install's output as %q; want its line as the step's output, never as ocel's", said)
	}
	if said := outputs(lines, progressv1.Stream_STREAM_STDERR); !slices.Equal(said, []string{"npm warn deprecated left-pad"}) || len(stderr) != 0 {
		t.Errorf("before ocel started the job reported the install's stderr as the output %q and the run's stderr %q; want it the step's output alone", said, stderr)
	}
	if beats == 0 {
		t.Error("the job beat not once while it installed")
	}
}

func TestAnInstallThatFailsFailsTheJobWithTheLastOfItsStderr(t *testing.T) {
	fake := runJob(t, npmProject, "echo 'npm warn deprecated left-pad' >&2\necho 'npm error missing: left-pad' >&2\nexit 1\n", nil)
	finished := fake.finished
	if want := "install the dependencies in the project's root with npm ci: exit status 1: npm warn deprecated left-pad · npm error missing: left-pad"; finished.GetErrorCode() != runner.CodeRun || finished.GetExitCode() != 1 || finished.GetError() != want {
		t.Fatalf("the job finished %v; want the install's failure, told %q", finished, want)
	}
	if fake.ocelRan() {
		t.Error("ocel ran after the install failed")
	}
}

func TestAnInstallRunsWithoutTheJobTokenOrTheJobContextAndTheRunWithoutTheTokenVariable(t *testing.T) {
	seen := t.TempDir()
	fake := runJob(t, npmProject, "env > \"$SEEN/install\"\n", func(config *Config, f *fakeRunner) {
		config.Env = append(config.Env, "SEEN="+seen, runner.TokenEnvVar+"=the-token", "HTTPS_PROXY=http://proxy.example.com:3128")
		config.Ocel = writeScript(t, "#!/bin/sh\ntouch \"$RAN\"\nenv > \"$SEEN/run\"\n")
	})
	if !fake.ocelRan() {
		t.Fatalf("the job finished %v without running ocel", fake.finished)
	}
	install := readEnv(t, filepath.Join(seen, "install"))
	for _, leak := range []string{runner.TokenEnvVar + "=", runner.ContextEnvVar + "="} {
		if slices.ContainsFunc(install, func(kv string) bool { return strings.HasPrefix(kv, leak) }) {
			t.Errorf("the install ran with %s…: %q", leak, install)
		}
	}
	if slices.ContainsFunc(install, func(kv string) bool { return strings.Contains(kv, "the-token") }) {
		t.Errorf("the install's env holds the job's token: %q", install)
	}
	for _, kept := range []string{"HOME=", "PATH=", "HTTPS_PROXY="} {
		if !slices.ContainsFunc(install, func(kv string) bool { return strings.HasPrefix(kv, kept) }) {
			t.Errorf("the install ran without %s…: %q", kept, install)
		}
	}
	run := readEnv(t, filepath.Join(seen, "run"))
	if slices.ContainsFunc(run, func(kv string) bool { return strings.Contains(kv, "the-token") }) {
		t.Errorf("ocel's run has the job's token in its environment: %q", run)
	}
	if !slices.ContainsFunc(run, func(kv string) bool { return strings.HasPrefix(kv, runner.ContextEnvVar+"=") }) {
		t.Errorf("ocel's run was told no job context: %q", run)
	}
}

func writeScript(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ocel")
	write(t, path, content)
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func readEnv(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(raw)), "\n")
}

func TestAJobsStepsReportNoTelemetryWhateverTheAgentInherits(t *testing.T) {
	a := &agent{config: Config{Env: []string{"PATH=/usr/bin", "OCEL_TELEMETRY=1", "DO_NOT_TRACK=0"}}}
	last := func(env []string, key string) string {
		value := ""
		for _, kv := range env {
			if v, ok := strings.CutPrefix(kv, key+"="); ok {
				value = v
			}
		}
		return value
	}
	for name, env := range map[string][]string{
		"the install": a.installEnv("/job/home"),
		"ocel's run":  a.runEnv("/job", "/job/home"),
	} {
		if got := last(env, "OCEL_TELEMETRY"); got != "0" {
			t.Errorf("%s runs with OCEL_TELEMETRY=%q, want 0", name, got)
		}
		if got := last(env, "DO_NOT_TRACK"); got != "1" {
			t.Errorf("%s runs with DO_NOT_TRACK=%q, want 1", name, got)
		}
	}
}

func TestACancelDuringTheInstallStopsIt(t *testing.T) {
	started := time.Now()
	fake := runJob(t, npmProject, "exec sleep 30\n", func(_ *Config, f *fakeRunner) { f.cancel = true })
	if took := time.Since(started); took > 5*time.Second {
		t.Fatalf("the canceled job took %s to end", took)
	}
	if fake.finished == nil || fake.ocelRan() {
		t.Fatalf("the canceled job finished %v, ocel run %t; want it finished with no run", fake.finished, fake.ocelRan())
	}
}

func TestAnInstallPastTheTimeoutFinishesAsTimedOut(t *testing.T) {
	fake := runJob(t, npmProject, "exec sleep 30\n", func(config *Config, _ *fakeRunner) { config.Timeout = 300 * time.Millisecond })
	if fake.finished.GetErrorCode() != runner.CodeTimeout || fake.ocelRan() {
		t.Fatalf("the job finished %v, ocel run %t; want it stopped at its timeout", fake.finished, fake.ocelRan())
	}
}

func TestAGrandchildHoldingTheInstallsOutputHoldsTheJobNoLongerThanTheGrace(t *testing.T) {
	started := time.Now()
	fake := runJob(t, npmProject, "sleep 5 &\necho installed\n", func(config *Config, _ *fakeRunner) { config.StopGrace = 200 * time.Millisecond })
	if took := time.Since(started); took > 3*time.Second {
		t.Fatalf("the job took %s, held by what the install left running", took)
	}
	if !fake.ocelRan() || fake.finished.GetError() != "" {
		t.Fatalf("the job finished %v, ocel run %t; want the install done and ocel run", fake.finished, fake.ocelRan())
	}
}

func TestAnInstallThatCannotBeFoundFailsTheJobBeforeAnyStep(t *testing.T) {
	fake := runJob(t, npmProject, "touch \"$RAN\"\n", func(config *Config, _ *fakeRunner) {
		config.FindInstalls = func(context.Context, string, string) ([]Install, error) {
			return nil, errors.New("the dependencies of the project's root are not installed")
		}
	})
	if fake.finished.GetErrorCode() != runner.CodeSource || fake.finished.GetError() != "the dependencies of the project's root are not installed" || fake.ocelRan() {
		t.Fatalf("the job finished %v, ocel run %t; want the install refused before any step", fake.finished, fake.ocelRan())
	}
}

func TestAConfigFileOutsideTheProjectFailsTheJobBeforeAnyStep(t *testing.T) {
	for _, configFile := range []string{"../ocel.json", "/etc/ocel.json", "apps/../../ocel.json", `..\ocel.json`} {
		fake := runJobAs(t, npmProject, "touch \"$RAN\"\n", nil, func(job *runnerv1.JobRequest) { job.ConfigFile = configFile })
		if fake.finished.GetErrorCode() != runner.CodeSource || !strings.Contains(fake.finished.GetError(), "config file") || fake.ocelRan() {
			t.Errorf("config file %q: the job finished %v, ocel run %t; want it refused before any step", configFile, fake.finished, fake.ocelRan())
		}
	}
}

func TestAStepsBinaryIsFoundOnTheJobsPATHAlone(t *testing.T) {
	bin := t.TempDir()
	write(t, filepath.Join(bin, "npm"), "#!/bin/sh\n")
	if err := os.Chmod(filepath.Join(bin, "npm"), 0o755); err != nil {
		t.Fatal(err)
	}
	if found, err := lookPath("npm", []string{"PATH=/nonexistent", "PATH=" + bin}); err != nil || found != filepath.Join(bin, "npm") {
		t.Fatalf("lookPath = %q, %v; want the job's npm", found, err)
	}
	if found, err := lookPath("sh", []string{"PATH=" + bin}); err == nil {
		t.Fatalf("lookPath found %q, which only the agent's PATH holds", found)
	}
	if found, err := lookPath("sh", nil); err == nil {
		t.Fatalf("lookPath found %q with no PATH in the job's env", found)
	}
}

type slowTransport struct{ delay time.Duration }

func (s slowTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	time.Sleep(s.delay)
	return http.DefaultTransport.RoundTrip(request)
}

func TestTheJobBeatsWhileItsSourceIsStillDownloading(t *testing.T) {
	fake := runJob(t, map[string]string{"ocel.json": `{"slug":"web"}`}, "exit 1\n", func(config *Config, _ *fakeRunner) {
		config.HTTP = &http.Client{Transport: slowTransport{delay: 400 * time.Millisecond}}
	})
	if !fake.ocelRan() {
		t.Fatalf("the job finished %v without running ocel", fake.finished)
	}
	beats := 0
	for _, r := range fake.beforeOcel() {
		if len(r.lines)+len(r.stderr) == 0 {
			beats++
		}
	}
	if beats < 2 {
		t.Errorf("the job beat %d times while a blob took 400ms to download with a beat every 40ms, want it to keep beating", beats)
	}
}
