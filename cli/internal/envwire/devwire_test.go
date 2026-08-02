package envwire

import (
	"context"
	"net"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/devserver"
	"github.com/ocelhq/ocel/cli/internal/envgate"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
)

// The deploy path and the dev path each spawn node for the same discovery run,
// and until they shared one spawner they carried their own flag lists and their
// own copies of the two variable names the SDK reads. This test is the dev half
// of the exchange the file above pins for deploy: the same fixture, the same
// real SDK in a real node process, but started by devserver's own code path and
// answered by dev's own store.
func TestDefineEnv_DeclaresThroughDevserversOwnNodeSpawn(t *testing.T) {
	root := setUpFixture(t, envFixture)

	srv := serveDevServer(t)
	srv.UseValues(map[string]string{
		"PUBLIC_SITE_URL": "https://example.com",
		"PORT":            "80",
		"DB_PASSWORD":     "hunter2",
		"POSTHOG_ID":      "ph_everywhere",
	}, envgate.Scope{Apps: []envgate.App{
		{Name: "web", Folder: "/web"},
		{Name: "admin", Folder: "/admin"},
	}})

	cfg := &projectconfig.Config{
		Slug:      "devwire",
		Dir:       root,
		Discovery: projectconfig.Discovery{Paths: []string{"ocel"}},
	}

	var stdout, stderr strings.Builder
	if err := srv.Discover(context.Background(), cfg, &stdout, &stderr); err != nil {
		t.Fatalf("discovery: %v\nstdout: %s\nstderr: %s", err, stdout.String(), stderr.String())
	}
	<-srv.Sync()

	definitions := byKey(t, srv.Definitions())

	// A rename of OCEL_PHASE or OCEL_DEV_SERVER at this call site leaves the SDK
	// either never declaring or unable to reach the server, and this set empty.
	t.Run("every declared key arrives", func(t *testing.T) {
		var keys []string
		for key := range definitions {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		want := []string{"DB_PASSWORD", "LOG_LEVEL", "PORT", "POSTHOG_ID", "PUBLIC_SITE_URL", "STRIPE_API_KEY"}
		if strings.Join(keys, ",") != strings.Join(want, ",") {
			t.Fatalf("declared keys = %v, want %v", keys, want)
		}
	})

	t.Run("source names the file the user wrote", func(t *testing.T) {
		source := filepath.Join(root, "ocel", "env.ts")
		for key, definition := range definitions {
			if got := definition.GetSource(); got != source {
				t.Errorf("%s source = %q, want %q", key, got, source)
			}
		}
	})

	t.Run("the verdict is exactly the cells dev's store leaves short", func(t *testing.T) {
		err := srv.CheckEnv(context.Background())
		refusal, ok := err.(*envgate.Refusal)
		if !ok {
			t.Fatalf("CheckEnv() = %v, want a *Refusal", err)
		}
		got := describeProblems(refusal.Problems)
		want := []string{
			"PORT@ KIND_INVALID",
			"STRIPE_API_KEY@ KIND_MISSING",
		}
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Fatalf("problems =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
		}
	})
}

// serveDevServer stands up the production dev server on a loopback port, so the
// address discovery is told to talk to is the one the server itself holds.
func serveDevServer(t *testing.T) *devserver.Server {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := devserver.New("http://"+listener.Addr().String(), "tok", "proj", "http://"+listener.Addr().String())
	httpSrv := &http.Server{Handler: srv.Mux()}
	go httpSrv.Serve(listener)
	t.Cleanup(func() { httpSrv.Close() })
	return srv
}
