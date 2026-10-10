package variables_test

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/discovery"

	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/devresources"
	"github.com/ocelhq/ocel/cli/internal/devserver"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/variables"
)

func TestDefineEnvDeclaresThroughTheDevServer(t *testing.T) {
	t.Run("declares through devserver's own node spawn", func(t *testing.T) {
		root := setUpFixture(t, envFixture)

		srv := serveDevServer(t)
		srv.UseValues(map[string]string{
			"NEXT_PUBLIC_SITE_URL": "https://example.com",
			"PORT":                 "80",
			"DB_PASSWORD":          "hunter2",
			"POSTHOG_ID":           "ph_everywhere",
		}, variables.Scope{Apps: []variables.App{
			{Name: "web", Folder: "/web"},
			{Name: "admin", Folder: "/admin"},
		}})

		cfg := &project.Project{
			Slug:           "devserver",
			Dir:            root,
			DiscoveryPaths: []string{filepath.Base(clitest.DiscoveryDir(root))},
		}

		var stdout, stderr strings.Builder
		if err := discover(cfg, srv, &stdout, &stderr); err != nil {
			t.Fatalf("discovery: %v\nstdout: %s\nstderr: %s", err, stdout.String(), stderr.String())
		}
		<-srv.SyncResults()

		t.Run("a declared scope arrives with its folders", func(t *testing.T) {
			scoped := srv.ScopedFolders()
			if got := strings.Join(scoped["POSTHOG_ID"], ","); got != "/admin,/web" {
				t.Errorf("POSTHOG_ID folders = %q, want /admin,/web", got)
			}
			if _, ok := scoped["PORT"]; ok {
				t.Errorf("PORT is scoped to %v, want no folders", scoped["PORT"])
			}
		})

		t.Run("a NEXT_PUBLIC_ declaration arrives as a public key", func(t *testing.T) {
			if want := []string{"NEXT_PUBLIC_SITE_URL"}; !slices.Equal(srv.PublicKeys(), want) {
				t.Errorf("public keys = %v, want %v — only what was declared, since the deployment url is offered per app, where ocel writes it for that app's runtime", srv.PublicKeys(), want)
			}
		})

		t.Run("the verdict is exactly the cells dev's store leaves short", func(t *testing.T) {
			err := srv.RefuseIncompleteEnv(context.Background())
			refusal := &variables.MissingError{}
			ok := errors.As(err, &refusal)
			if !ok {
				t.Fatalf("RefuseIncompleteEnv() = %v, want a *Refusal", err)
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

func discover(cfg *project.Project, srv *devserver.Server, stdout, stderr io.Writer) error {
	roots, err := discovery.RootsOf(cfg)
	if err != nil {
		return err
	}
	prepared, err := discovery.Prepare(cfg.Dir, roots)
	if err != nil {
		return err
	}
	if err := discovery.Run(context.Background(), cfg.Dir, prepared, srv.DiscoveryTarget(), stdout, stderr); err != nil {
		return err
	}
	return srv.TakeSDKRefusal()
}

func serveDevServer(t *testing.T) *devserver.Server {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := devserver.New("http://"+listener.Addr().String(), devresources.New("defineenv", devresources.Options{}))
	httpSrv := &http.Server{Handler: srv.Mux()}
	go httpSrv.Serve(listener)
	t.Cleanup(func() { httpSrv.Close() })
	return srv
}
