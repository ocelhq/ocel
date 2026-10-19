package pythonserver

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/containerimage"
)

const wsgiHello = `def %s(environ, start_response):
    start_response("200 OK", [("Content-Type", "text/plain")])
    return [b"hello from " + environ["PATH_INFO"].encode()]
`

func serverApp(t *testing.T, files map[string]string) string {
	t.Helper()
	if _, err := exec.LookPath(containerimage.PythonWorkerEntry.Interpreter); err != nil {
		t.Skip("no python interpreter on PATH to run the server program with")
	}
	dir := appDir(t, files)
	if err := os.WriteFile(filepath.Join(dir, File), []byte(Source), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

func commandFor(t *testing.T, dir, entrypoint string) []string {
	t.Helper()
	resolved, err := ResolveEntrypoint(dir, entrypoint)
	if err != nil {
		t.Fatalf("ResolveEntrypoint(%q) = %v", entrypoint, err)
	}
	return resolved.Command()
}

func startServer(t *testing.T, dir, entrypoint string, env ...string) (int, *bytes.Buffer) {
	t.Helper()
	port := freePort(t)
	ctx, cancel := context.WithCancel(context.Background())
	command := commandFor(t, dir, entrypoint)
	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	cmd.Dir = dir
	cmd.Env = append(append(os.Environ(), "PORT="+strconv.Itoa(port)), env...)
	var said bytes.Buffer
	cmd.Stdout = &said
	cmd.Stderr = &said
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		_ = cmd.Wait()
	})
	return port, &said
}

func get(t *testing.T, port int, path string, said *bytes.Buffer) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		resp, err := http.Get("http://127.0.0.1:" + strconv.Itoa(port) + path)
		if err == nil {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			return string(body)
		}
		if time.Now().After(deadline) {
			t.Fatalf("nothing answered on port %d: %v\nthe server program said:\n%s", port, err, said.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func runServer(t *testing.T, dir, entrypoint string) (string, error) {
	t.Helper()
	command := commandFor(t, dir, entrypoint)
	cmd := exec.Command(command[0], command[1:]...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "PORT="+strconv.Itoa(freePort(t)))
	said, err := cmd.CombinedOutput()
	return string(said), err
}

func TestTheServerProgramServesTheAppAWSGIEntrypointDefines(t *testing.T) {
	t.Parallel()

	dir := serverApp(t, map[string]string{"app.py": fmt.Sprintf(wsgiHello, "app")})
	port, said := startServer(t, dir, "app.py")

	if body := get(t, port, "/orders", said); body != "hello from /orders" {
		t.Errorf("GET /orders = %q, want the app's own answer", body)
	}
}

func TestTheServerProgramServesTheAppWhenPythonLeavesItsDirectoryOffThePath(t *testing.T) {
	t.Parallel()

	dir := serverApp(t, map[string]string{"app.py": fmt.Sprintf(wsgiHello, "app")})
	port, said := startServer(t, dir, "app.py", "PYTHONSAFEPATH=1")

	if body := get(t, port, "/safe", said); body != "hello from /safe" {
		t.Errorf("GET /safe = %q, want the app served from the directory the server program is in", body)
	}
}

func TestTheServerProgramServesApplicationWhenAnEntrypointDefinesNoApp(t *testing.T) {
	t.Parallel()

	dir := serverApp(t, map[string]string{"wsgi.py": fmt.Sprintf(wsgiHello, "application")})
	port, said := startServer(t, dir, "wsgi.py")

	if body := get(t, port, "/", said); body != "hello from /" {
		t.Errorf("GET / = %q, want what application answers, the name Django's wsgi.py defines", body)
	}
}

func TestTheServerProgramServesTheObjectAModuleEntrypointNames(t *testing.T) {
	t.Parallel()

	dir := serverApp(t, map[string]string{
		"shop/__init__.py": "",
		"shop/api.py":      fmt.Sprintf(wsgiHello, "handler") + "app = None\n",
	})
	port, said := startServer(t, dir, "shop.api:handler")

	if body := get(t, port, "/cart", said); body != "hello from /cart" {
		t.Errorf("GET /cart = %q, want what shop.api:handler answers", body)
	}
}

func TestTheServerProgramServesAnEntrypointUnderSrcThatImportsItsSiblings(t *testing.T) {
	t.Parallel()

	dir := serverApp(t, map[string]string{
		"src/greeting.py": "WORD = b'hello from '\n",
		"src/main.py": `from greeting import WORD


def app(environ, start_response):
    start_response("200 OK", [])
    return [WORD + environ["PATH_INFO"].encode()]
`,
	})
	port, said := startServer(t, dir, "src/main.py")

	if body := get(t, port, "/src", said); body != "hello from /src" {
		t.Errorf("GET /src = %q, want the app answering with what it imported from beside it", body)
	}
}

func TestTheServerProgramNeverRunsTheMainBlockOfAnAppItServes(t *testing.T) {
	t.Parallel()

	dir := serverApp(t, map[string]string{"main.py": fmt.Sprintf(wsgiHello, "app") + `

if __name__ == "__main__":
    raise SystemExit("the main block ran")
`})
	port, said := startServer(t, dir, "main.py")

	if body := get(t, port, "/", said); body != "hello from /" {
		t.Errorf("GET / = %q, want the app served — an app's own main block starts a development server ocel replaces", body)
	}
}

func TestTheServerProgramRunsAnEntrypointDefiningNoAppAsAScript(t *testing.T) {
	t.Parallel()

	dir := serverApp(t, map[string]string{"main.py": `import os
from http.server import BaseHTTPRequestHandler, HTTPServer


class Hello(BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(200)
        self.end_headers()
        self.wfile.write(b"served by " + __name__.encode())


if __name__ == "__main__":
    HTTPServer(("127.0.0.1", int(os.environ["PORT"])), Hello).serve_forever()
`})
	port, said := startServer(t, dir, "main.py")

	if body := get(t, port, "/", said); body != "served by __main__" {
		t.Errorf("GET / = %q, want the script serving itself as __main__, as python main.py runs it", body)
	}
}

func TestTheServerProgramSaysAScriptThatReturnedServedNothing(t *testing.T) {
	t.Parallel()

	dir := serverApp(t, map[string]string{"main.py": "print('ran as ' + __name__)\n"})
	said, err := runServer(t, dir, "main.py")

	if err == nil || !strings.Contains(said, "ran as __main__") || !strings.Contains(said, "neither app nor application") {
		t.Errorf("the server program exited with %v saying:\n%s\nwant the script run as __main__ and then a failure saying it defines no app to serve", err, said)
	}
}

func TestTheServerProgramRefusesAnASGIAppWithNoUvicornToServeIt(t *testing.T) {
	t.Parallel()

	dir := serverApp(t, map[string]string{"asgi.py": `async def app(scope, receive, send):
    pass
`})
	if exec.Command(containerimage.PythonWorkerEntry.Interpreter, "-c", "import uvicorn").Run() == nil {
		t.Skip("the python on PATH has uvicorn, which serves the app")
	}
	said, err := runServer(t, dir, "asgi.py")

	if err == nil || !strings.Contains(said, "ASGI") || !strings.Contains(said, "uvicorn") {
		t.Errorf("the server program exited with %v saying:\n%s\nwant a failure naming the ASGI app and uvicorn, the server it needs", err, said)
	}
}

func TestTheServerProgramRefusesAModuleEntrypointThatIsNotOnThePath(t *testing.T) {
	t.Parallel()

	dir := serverApp(t, map[string]string{"main.py": "print('hi')\n"})
	said, err := runServer(t, dir, "missing.api:app")

	if err == nil || !strings.Contains(said, "missing.api") {
		t.Errorf("the server program exited with %v saying:\n%s\nwant a failure naming the module it could not find", err, said)
	}
}

func TestTheServerCommandStatesWhetherTheEntrypointIsAFileOrAModule(t *testing.T) {
	t.Parallel()

	for entrypoint, want := range map[Entrypoint][]string{
		{File: "src/main.py"}:    {containerimage.PythonWorkerEntry.Interpreter, File, "file", "src/main.py"},
		{Module: "shop.api:app"}: {containerimage.PythonWorkerEntry.Interpreter, File, "module", "shop.api:app"},
	} {
		if got := entrypoint.Command(); !slices.Equal(got, want) {
			t.Errorf("%+v.Command() = %q, want %q — the server program serves the form the build resolved rather than guessing it again", entrypoint, got, want)
		}
	}
}

func TestTheServerProgramServesAFileEntrypointItIsToldIsAFile(t *testing.T) {
	t.Parallel()

	dir := serverApp(t, map[string]string{"api/v1.py": fmt.Sprintf(wsgiHello, "app")})
	port, said := startServer(t, dir, "api/v1.py")

	if body := get(t, port, "/v1", said); body != "hello from /v1" {
		t.Errorf("GET /v1 = %q, want the app the file defines", body)
	}
}

func TestTheServerProgramRefusesAnEntrypointWithNoForm(t *testing.T) {
	t.Parallel()

	dir := serverApp(t, map[string]string{"main.py": fmt.Sprintf(wsgiHello, "app")})
	said, err := exec.Command(containerimage.PythonWorkerEntry.Interpreter, filepath.Join(dir, File), "main.py").CombinedOutput()

	if err == nil || !strings.Contains(string(said), "file or module") {
		t.Errorf("the server program exited with %v saying:\n%s\nwant a refusal naming the forms it takes", err, said)
	}
}

func TestDefinesAppReportsWhetherTheServerProgramServesAnAppOrRunsAScript(t *testing.T) {
	t.Parallel()

	dir := serverApp(t, map[string]string{
		"app.py":           fmt.Sprintf(wsgiHello, "app"),
		"wsgi.py":          fmt.Sprintf(wsgiHello, "application"),
		"script.py":        "import hypercorn\n\nif __name__ == '__main__':\n    raise SystemExit('ran')\n",
		"shop/__init__.py": "",
		"shop/api.py":      fmt.Sprintf(wsgiHello, "handler"),
		"submodule.py":     "import app.views\nimport app\n\nif __name__ == '__main__':\n    raise SystemExit('ran')\n",
		"imported.py":      "from shop.api import handler as app\n",
		"aliased.py":       "import shop.api as application\n",
		"annotated.py":     "app: object = object()\n",
		"augmented.py":     "app += [1]\n",
	})
	for entrypoint, want := range map[Entrypoint]bool{
		{File: "app.py"}:             true,
		{File: "wsgi.py"}:            true,
		{File: "script.py"}:          false,
		{Module: "shop.api"}:         false,
		{Module: "shop.api:handler"}: true,
		{File: "submodule.py"}:       false,
		{File: "imported.py"}:        true,
		{File: "aliased.py"}:         true,
		{File: "annotated.py"}:       true,
		{File: "augmented.py"}:       true,
	} {
		got, err := entrypoint.DefinesApp(context.Background(), containerimage.PythonWorkerEntry.Interpreter, dir)
		if err != nil || got != want {
			t.Errorf("%+v.DefinesApp() = %t, %v, want %t — the build asks what the server program decides at boot", entrypoint, got, err, want)
		}
	}
}
