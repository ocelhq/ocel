package devserver

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	connect "connectrpc.com/connect"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	"github.com/ocelhq/ocel/pkg/proto/app/resources/v1/resourcesv1connect"
)

func serveResources(t *testing.T) resourcesv1connect.ResourceServiceClient {
	t.Helper()
	ts := httptest.NewServer(New("http://127.0.0.1:0", "leader-tok", "proj_1", "http://127.0.0.1:0").Mux())
	t.Cleanup(ts.Close)
	return resourcesv1connect.NewResourceServiceClient(http.DefaultClient, ts.URL)
}

func TestDeclareEnvFolderScopes(t *testing.T) {
	t.Parallel()

	refused := map[string][]string{
		"a folder that is not anchored at the root":  {"web"},
		"a folder carrying the store key delimiter":  {"/we#b"},
		"a folder with an empty path segment":        {"//web"},
		"a folder spelled with a trailing separator": {"/web/"},
		"the root spelled as a folder":               {"/"},
		"the same folder named twice":                {"/web", "/web"},
	}
	for name, folders := range refused {
		t.Run(name+" is refused", func(t *testing.T) {
			t.Parallel()
			_, err := serveResources(t).DeclareEnv(context.Background(), &resourcesv1.DeclareEnvRequest{
				Definitions: []*resourcesv1.VariableDefinition{{
					Key:     "POSTHOG_ID",
					Class:   resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN,
					Folders: folders,
				}},
			})

			var connectErr *connect.Error
			if !errors.As(err, &connectErr) || connectErr.Code() != connect.CodeInvalidArgument {
				t.Fatalf("DeclareEnv(%v) err = %v, want %v", folders, err, connect.CodeInvalidArgument)
			}
		})
	}

	t.Run("two distinct folders are admitted", func(t *testing.T) {
		t.Parallel()
		if _, err := serveResources(t).DeclareEnv(context.Background(), &resourcesv1.DeclareEnvRequest{
			Definitions: []*resourcesv1.VariableDefinition{{
				Key:     "POSTHOG_ID",
				Class:   resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN,
				Folders: []string{"/web", "/web/admin"},
			}},
		}); err != nil {
			t.Fatalf("DeclareEnv = %v, want a well formed scope admitted", err)
		}
	})
}
