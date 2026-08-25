package envwiretest

import (
	"context"
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/devserver"
	"github.com/ocelhq/ocel/cli/internal/envgate"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
)

func TestDevserverDiscover(t *testing.T) {
	t.Run("declares through devserver's own node spawn", func(t *testing.T) {
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
			Slug:      "devserver",
			Dir:       root,
			Discovery: projectconfig.Discovery{Paths: []string{"ocel"}},
		}

		var stdout, stderr strings.Builder
		if err := srv.Discover(context.Background(), cfg, &stdout, &stderr); err != nil {
			t.Fatalf("discovery: %v\nstdout: %s\nstderr: %s", err, stdout.String(), stderr.String())
		}
		<-srv.Sync()

		t.Run("a declared scope arrives with its folders", func(t *testing.T) {
			scoped := srv.ScopedFolders()
			if got := strings.Join(scoped["POSTHOG_ID"], ","); got != "/admin,/web" {
				t.Errorf("POSTHOG_ID folders = %q, want /admin,/web", got)
			}
			if _, ok := scoped["PORT"]; ok {
				t.Errorf("PORT is scoped to %v, want no folders", scoped["PORT"])
			}
		})

		t.Run("a client-accessible declaration arrives as one", func(t *testing.T) {
			if got := strings.Join(srv.ClientKeys(), ","); got != "PUBLIC_SITE_URL" {
				t.Errorf("client keys = %q, want PUBLIC_SITE_URL", got)
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
	})
}

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
