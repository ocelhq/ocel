package build

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/ocelhq/ocel/cli/internal/project"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
)

func TestANextBuildThatPrintsABindingsPasswordAloneSaysItNowhere(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeBuildScript(t, root)
	cfg := &project.Project{Dir: root, Apps: []project.App{nextApp("web", "apps/web")}}
	encoded, err := protojson.Marshal(&bindingsv1.Binding{
		Name: "main",
		Properties: &bindingsv1.Binding_Postgres{Postgres: &bindingsv1.PostgresProperties{
			Host: "127.0.0.1", Port: 41234, Username: "app", Password: "pg-forwarded-password", Database: "main",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var shared bytes.Buffer
	builder := nodeOnly{host: servingNext, node: func(_ context.Context, _ string, _ []byte, log Log) error {
		_, err := io.WriteString(log.shared(), "connecting as app with pg-forwarded-password\n")
		return err
	}}

	if err := builder.Build(context.Background(), cfg, map[string]AppVariables{"web": {Live: map[string]string{"OCEL_RESOURCE_POSTGRES_main": string(encoded)}}}, Log{Shared: &shared}); err != nil {
		t.Fatalf("Build: %v", err)
	}

	if said := shared.String(); strings.Contains(said, "pg-forwarded-password") || !strings.Contains(said, "connecting as app") {
		t.Errorf("the build said %q, want the binding's password hidden wherever it is printed on its own", said)
	}
}
