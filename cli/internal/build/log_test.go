package build

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/nodeprotocol"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
)

type appSpans struct {
	mu     sync.Mutex
	shared strings.Builder
	opened []string
	logs   map[string]*strings.Builder
	ended  map[string]error
}

type lockedWriter struct {
	mu *sync.Mutex
	w  io.Writer
}

func (l lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

func (u *appSpans) output() Log {
	return Log{
		Shared: lockedWriter{&u.mu, &u.shared},
		AppLog: func(name string) (io.Writer, func(error)) {
			u.mu.Lock()
			defer u.mu.Unlock()
			if u.logs == nil {
				u.logs, u.ended = map[string]*strings.Builder{}, map[string]error{}
			}
			u.opened = append(u.opened, name)
			log := &strings.Builder{}
			u.logs[name] = log
			return lockedWriter{&u.mu, log}, func(err error) {
				u.mu.Lock()
				defer u.mu.Unlock()
				if _, twice := u.ended[name]; twice {
					panic("span " + name + " ended twice")
				}
				u.ended[name] = err
			}
		},
	}
}

func (u *appSpans) log(name string) string {
	u.mu.Lock()
	defer u.mu.Unlock()
	if log, ok := u.logs[name]; ok {
		return log.String()
	}
	return ""
}

func nodeBuilder(t *testing.T, script string) *project.Project {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not on PATH")
	}
	root := t.TempDir()
	writeBuildScript(t, root)
	emit := fmt.Sprintf(`const emit = (r) => process.stdout.write("\n" + %s + JSON.stringify(r) + "\n");
const req = JSON.parse(require("fs").readFileSync(0, "utf8"));
`, jsString(nodeprotocol.Prefix))
	if err := os.WriteFile(testBuildScript(root), []byte(emit+script), 0o644); err != nil {
		t.Fatal(err)
	}
	return &project.Project{
		Dir:  root,
		Apps: []project.App{nextApp("web", "."), nextApp("api", ".")},
	}
}

func TestEachJavaScriptAppsBuildOutputReachesTheSpanOpenedForThatAppWhileTheBuildsOverlap(t *testing.T) {
	t.Parallel()
	cfg := nodeBuilder(t, `
console.log("before any app");
for (const app of req.apps) emit({type: "span_start", id: app.name, stage: "build", app: app.name});
for (const app of req.apps) emit({type: "output", app: app.name, message: "compiling " + app.name});
console.log("said by no app");
for (const app of req.apps) emit({type: "output", app: app.name, message: "a warning while building " + app.name});
for (const app of req.apps) emit({type: "span_end", id: app.name, ok: true});
`)

	var spans appSpans
	if err := (nodeOnly{node: runNode}).Build(context.Background(), cfg, nil, spans.output()); err != nil {
		t.Fatalf("Build: %v", err)
	}

	if got := strings.Join(spans.opened, ","); got != "web,api" {
		t.Errorf("spans opened = %s, want web then api", got)
	}
	for _, app := range []string{"web", "api"} {
		want := "compiling " + app + "\na warning while building " + app + "\n"
		if got := spans.log(app); got != want {
			t.Errorf("%s's log = %q, want only its own lines %q", app, got, want)
		}
		if err, ok := spans.ended[app]; !ok || err != nil {
			t.Errorf("%s ended = %v (ended %t), want it ended without error", app, err, ok)
		}
	}
	if got := spans.shared.String(); !strings.Contains(got, "before any app") || !strings.Contains(got, "said by no app") || strings.Contains(got, "compiling") {
		t.Errorf("shared output = %q, want only what no app's build said", got)
	}
}

func TestAJavaScriptAppWhoseBuildFailsEndsItsSpanWithTheFailureTheBuilderReported(t *testing.T) {
	t.Parallel()
	cfg := nodeBuilder(t, `
const app = req.apps[0].name;
emit({type: "span_start", id: "1", stage: "build", app});
console.log("compiling " + app);
emit({type: "error", stage: "build", app, message: "Error: no entrypoint resolved\n    at build (builder.js:1:1)"});
emit({type: "span_end", id: "1", ok: false});
process.exitCode = 1;
`)

	var spans appSpans
	if err := (nodeOnly{node: runNode}).Build(context.Background(), cfg, nil, spans.output()); err == nil {
		t.Fatal("Build succeeded, want the builder's failure")
	}
	err, ok := spans.ended["web"]
	if !ok || err == nil || err.Error() != "Error: no entrypoint resolved" {
		t.Errorf("web ended with %v (ended %t), want the first line of the error the builder reported", err, ok)
	}
	if _, opened := spans.ended["api"]; opened {
		t.Error("api's span ended, want it never opened: the builder stopped at web")
	}
}

func TestAJavaScriptAppWhoseBuildFailsWithALiveValueEndsItsSpanWithTheValueHidden(t *testing.T) {
	t.Parallel()
	cfg := nodeBuilder(t, `
const app = req.apps[0].name;
emit({type: "span_start", id: "1", stage: "build", app});
emit({type: "error", stage: "build", app, message: "Error: could not reach postgres://u:hunter2@127.0.0.1/db"});
emit({type: "span_end", id: "1", ok: false});
process.exitCode = 1;
`)

	var spans appSpans
	err := (nodeOnly{node: runNode}).Build(context.Background(), cfg, map[string]AppVariables{
		"web": {Live: map[string]string{"OCEL_SECRET_DB": "postgres://u:hunter2@127.0.0.1/db"}},
	}, spans.output())
	if err == nil {
		t.Fatal("Build succeeded, want the builder's failure")
	}

	ended, ok := spans.ended["web"]
	if !ok || ended == nil || strings.Contains(ended.Error(), "hunter2") {
		t.Errorf("web ended with %v (ended %t), want the failure with the value hidden, since the terminal prints it as the span ends", ended, ok)
	}
}

func TestALiveValueAJavaScriptAppsLongLineCutsBetweenPiecesIsHiddenInItsLog(t *testing.T) {
	t.Parallel()
	cfg := nodeBuilder(t, `
const app = req.apps[0].name;
const line = "x".repeat(65530) + "sk_live_sensitive and more";
emit({type: "span_start", id: "1", stage: "build", app});
emit({type: "output", app, message: line.slice(0, 65536), partial: true});
emit({type: "output", app, message: line.slice(65536)});
emit({type: "span_end", id: "1", ok: true});
`)
	cfg.Apps = cfg.Apps[:1]

	var spans appSpans
	if err := (nodeOnly{node: runNode}).Build(context.Background(), cfg, map[string]AppVariables{
		"web": {Live: map[string]string{"STRIPE_API_KEY": "sk_live_sensitive"}},
	}, spans.output()); err != nil {
		t.Fatalf("Build: %v", err)
	}

	want := strings.Repeat("x", 65530) + "[secret] and more\n"
	if got := spans.log("web"); got != want {
		t.Errorf("web's log ends %q, want the line whole with the value hidden, ending %q", got[max(len(got)-40, 0):], want[len(want)-40:])
	}
}

func TestAJavaScriptAppsSpanStillEndsWhenTheBuilderDiesMidBuild(t *testing.T) {
	t.Parallel()
	cfg := nodeBuilder(t, `
emit({type: "span_start", id: "1", stage: "build", app: "web"});
process.exit(9);
`)

	var spans appSpans
	if err := (nodeOnly{node: runNode}).Build(context.Background(), cfg, nil, spans.output()); err == nil {
		t.Fatal("Build succeeded, want the builder's exit")
	}
	if err, ok := spans.ended["web"]; !ok || err == nil {
		t.Errorf("web ended with %v (ended %t), want it ended in failure", err, ok)
	}
}

func TestAJavaScriptAppsSpanEndsInFailureWhenTheBuilderExitsCleanlyMidBuild(t *testing.T) {
	t.Parallel()
	cfg := nodeBuilder(t, `
emit({type: "span_start", id: "1", stage: "build", app: "web"});
process.exit(0);
`)

	var spans appSpans
	if err := (nodeOnly{node: runNode}).Build(context.Background(), cfg, nil, spans.output()); err == nil {
		t.Fatal("Build succeeded, want it failed: web's build never ended")
	}
	if err, ok := spans.ended["web"]; !ok || err == nil {
		t.Errorf("web ended with %v (ended %t), want it ended in failure", err, ok)
	}
}

func TestAGoAppCompiledHereBuildsInASpanOfItsOwnThatEndsWithItsCompileError(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeBuildScript(t, root)
	writeGoApp(t, root, "apps/api")
	if err := os.WriteFile(filepath.Join(root, "apps/api/main.go"), []byte("package main\n\nfunc main() {\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &project.Project{
		Dir:  root,
		Apps: []project.App{inGoModule(t, root, project.App{Name: "api", Path: "apps/api", Compute: provider.ComputeServerless, Framework: "go"})},
	}

	var spans appSpans
	err := nodeOnly{node: func(context.Context, string, []byte, Log) error { return nil }}.Build(context.Background(), cfg, nil, spans.output())
	if err == nil {
		t.Fatal("Build succeeded, want the compile error")
	}
	if got := strings.Join(spans.opened, ","); got != "api" {
		t.Errorf("spans opened = %s, want api's alone", got)
	}
	if ended, ok := spans.ended["api"]; !ok || ended == nil || ended.Error() != err.Error() {
		t.Errorf("api ended with %v (ended %t), want the compile error %q", ended, ok, err)
	}
}

type stepsSeen struct {
	mu   sync.Mutex
	seen []string
}

func (s *stepsSeen) add(line string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seen = append(s.seen, line)
}

func (s *stepsSeen) list() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.seen)
}

func (s *stepsSeen) output() Log {
	return Log{
		AppLog: func(app string) (io.Writer, func(error)) {
			s.add("open app " + app)
			return io.Discard, func(error) { s.add("end app " + app) }
		},
		Step: func(subject string, title progress.Title) func(error) {
			s.add("open " + subject + ": " + title.Started)
			return func(err error) {
				if err != nil {
					s.add("fail " + title.Started + ": " + err.Error())
					return
				}
				s.add("end " + title.Started)
			}
		},
	}
}

func nextProjectWithStaleOutput(t *testing.T) *project.Project {
	t.Helper()
	root := t.TempDir()
	writeBuildScript(t, root)
	writeFile(t, filepath.Join(outputRoot(t, root), "stale"), "from the last build")
	return &project.Project{Slug: "shop", Dir: root, Apps: []project.App{nextApp("web", ".")}}
}

func TestABuildNamesEveryStepBetweenItsAppsBuilds(t *testing.T) {
	t.Parallel()
	cfg := nextProjectWithStaleOutput(t)
	var steps stepsSeen
	builder := nodeOnly{node: func(_ context.Context, _ string, request []byte, log Log) error {
		var req nodeBuildRequest
		if err := json.Unmarshal(request, &req); err != nil {
			return err
		}
		_, ended := log.App("web")
		writeFuncConfig(t, filepath.Dir(filepath.Dir(req.Apps[0].OutputDir)), "web", "index.func",
			buildoutput.FunctionConfig{Framework: buildoutput.Framework{Name: "next"}, EntryFile: "index.handler", App: "web"})
		ended(nil)
		return nil
	}}

	if err := builder.Build(context.Background(), cfg, nil, steps.output()); err != nil {
		t.Fatalf("Build: %v", err)
	}

	want := []string{
		"open shop: Removing the previous build output in " + buildoutput.Dir,
		"end Removing the previous build output in " + buildoutput.Dir,
		"open shop: Loading the build adapters for app web",
		"end Loading the build adapters for app web",
		"open app web",
		"end app web",
		"open web: Checking the platform packages of app web",
		"end Checking the platform packages of app web",
	}
	if got := steps.list(); !slices.Equal(got, want) {
		t.Errorf("the build reported\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestANodeBuildThatFailsBeforeAnyAppStartsFailsTheLoadingOfItsAdapters(t *testing.T) {
	t.Parallel()
	cfg := nextProjectWithStaleOutput(t)
	var steps stepsSeen
	builder := nodeOnly{node: func(context.Context, string, []byte, Log) error {
		return errors.New("the node build failed: Cannot find module")
	}}

	if err := builder.Build(context.Background(), cfg, nil, steps.output()); err == nil {
		t.Fatal("Build succeeded, want the node build's failure")
	}

	if want := "fail Loading the build adapters for app web: the node build failed: Cannot find module"; !slices.Contains(steps.list(), want) {
		t.Errorf("the build reported %q, want %q", steps.list(), want)
	}
}
