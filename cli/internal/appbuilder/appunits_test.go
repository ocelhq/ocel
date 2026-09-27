package appbuilder

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/nodeprotocol"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/node"
)

type appUnits struct {
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

func (u *appUnits) output() Output {
	return Output{
		Shared: lockedWriter{&u.mu, &u.shared},
		Unit: func(name string) (io.Writer, func(error)) {
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
					panic("unit " + name + " ended twice")
				}
				u.ended[name] = err
			}
		},
	}
}

func (u *appUnits) log(name string) string {
	u.mu.Lock()
	defer u.mu.Unlock()
	if log, ok := u.logs[name]; ok {
		return log.String()
	}
	return ""
}

func nodeBuilder(t *testing.T, script string) *projectconfig.Config {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not on PATH")
	}
	root := t.TempDir()
	writeBuilder(t, root)
	emit := fmt.Sprintf(`const emit = (r) => process.stdout.write("\n" + %s + JSON.stringify(r) + "\n");
const req = JSON.parse(require("fs").readFileSync(0, "utf8"));
require("fs").mkdirSync(req.outDir, {recursive: true});
require("fs").writeFileSync(require("path").join(req.outDir, %q), JSON.stringify({functions: []}));
`, jsString(nodeprotocol.Prefix), buildPlanFileName)
	if err := os.WriteFile(node.BuilderPath(root), []byte(emit+script), 0o644); err != nil {
		t.Fatal(err)
	}
	return &projectconfig.Config{
		Dir:  root,
		Apps: []projectconfig.App{{Name: "web", Path: "."}, {Name: "api", Path: "."}},
	}
}

func TestEachJavaScriptAppsBuildOutputReachesTheUnitOpenedForThatApp(t *testing.T) {
	t.Parallel()
	cfg := nodeBuilder(t, `
console.log("before any app");
for (const app of req.apps) {
  emit({type: "span_start", id: app.name, stage: "build", app: app.name});
  console.log("compiling " + app.name);
  console.error("a warning while building " + app.name);
  emit({type: "span_end", id: app.name, ok: true});
}
`)

	var units appUnits
	if err := Build(context.Background(), cfg, nil, units.output()); err != nil {
		t.Fatalf("Build: %v", err)
	}

	if got := strings.Join(units.opened, ","); got != "web,api" {
		t.Errorf("units opened = %s, want web then api", got)
	}
	for _, app := range []string{"web", "api"} {
		want := "compiling " + app + "\na warning while building " + app + "\n"
		if got := strings.TrimLeft(units.log(app), "\n"); !strings.HasPrefix(got, want) {
			t.Errorf("%s's log = %q, want its own stdout and stderr lines %q", app, units.log(app), want)
		}
		if err, ok := units.ended[app]; !ok || err != nil {
			t.Errorf("%s ended = %v (ended %t), want it ended without error", app, err, ok)
		}
	}
	if got := units.shared.String(); !strings.Contains(got, "before any app") || strings.Contains(got, "compiling") {
		t.Errorf("shared output = %q, want only what no app's build said", got)
	}
}

func TestAJavaScriptAppWhoseBuildFailsEndsItsUnitWithTheFailureTheBuilderReported(t *testing.T) {
	t.Parallel()
	cfg := nodeBuilder(t, `
const app = req.apps[0].name;
emit({type: "span_start", id: "1", stage: "build", app});
console.log("compiling " + app);
emit({type: "error", stage: "build", app, message: "Error: no entrypoint resolved\n    at build (builder.js:1:1)"});
emit({type: "span_end", id: "1", ok: false});
process.exitCode = 1;
`)

	var units appUnits
	if err := Build(context.Background(), cfg, nil, units.output()); err == nil {
		t.Fatal("Build succeeded, want the builder's failure")
	}
	err, ok := units.ended["web"]
	if !ok || err == nil || err.Error() != "Error: no entrypoint resolved" {
		t.Errorf("web ended with %v (ended %t), want the first line of the error the builder reported", err, ok)
	}
	if _, opened := units.ended["api"]; opened {
		t.Error("api's unit ended, want it never opened: the builder stopped at web")
	}
}

func TestAJavaScriptAppsUnitStillEndsWhenTheBuilderDiesMidBuild(t *testing.T) {
	t.Parallel()
	cfg := nodeBuilder(t, `
emit({type: "span_start", id: "1", stage: "build", app: "web"});
process.exit(9);
`)

	var units appUnits
	if err := Build(context.Background(), cfg, nil, units.output()); err == nil {
		t.Fatal("Build succeeded, want the builder's exit")
	}
	if err, ok := units.ended["web"]; !ok || err == nil {
		t.Errorf("web ended with %v (ended %t), want it ended in failure", err, ok)
	}
}

func TestAGoAppCompiledHereBuildsInAUnitOfItsOwnThatEndsWithItsCompileError(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeBuilder(t, root)
	writeGoApp(t, root, "apps/api")
	if err := os.WriteFile(filepath.Join(root, "apps/api/main.go"), []byte("package main\n\nfunc main() {\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &projectconfig.Config{
		Dir:  root,
		Apps: []projectconfig.App{{Name: "api", Path: "apps/api", Framework: projectconfig.Framework{Name: "go"}}},
	}

	var units appUnits
	err := Builder{Exec: func(context.Context, string, []string, []byte, Output) error { return nil }}.Build(context.Background(), cfg, nil, units.output())
	if err == nil {
		t.Fatal("Build succeeded, want the compile error")
	}
	if got := strings.Join(units.opened, ","); got != "api" {
		t.Errorf("units opened = %s, want api's alone", got)
	}
	if ended, ok := units.ended["api"]; !ok || ended == nil || ended.Error() != err.Error() {
		t.Errorf("api ended with %v (ended %t), want the compile error %q", ended, ok, err)
	}
}
