package conformance

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/edge"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/providerserver"
	"github.com/ocelhq/ocel/pkg/router"
)

func CheckContainerNeeds(t *testing.T, p provider.Provider, front edge.Kind, needs []edge.Need, allowDegraded ...string) (providerserver.AppNeedVerdict, error) {
	t.Helper()

	kind, paired := p.Facts().PairedRouter(front, provider.ComputeContainer)
	if !paired {
		t.Fatalf("%s pairs no router with a container", front)
	}
	routed, err := p.Routers().Open(kind)
	if err != nil {
		t.Fatalf("Routers().Open(%s) = %v", kind, err)
	}
	opened, err := p.Edges().Open(front, nil)
	if err != nil {
		t.Fatalf("Edges().Open(%s) = %v", front, err)
	}
	root := t.TempDir()
	dir := buildoutput.AppRoot(root, "web")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	hosting := buildoutput.Hosting{Version: buildoutput.HostingVersion, Needs: map[edge.Need]buildoutput.NeedDetail{}}
	for _, need := range needs {
		hosting.Needs[need] = buildoutput.NeedDetail{Count: 1}
	}
	raw, err := json.Marshal(hosting)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, buildoutput.HostingFile), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	verdicts, err := providerserver.EdgeNeedCheck{
		Edge:          opened,
		Router:        func(string) router.Router { return routed },
		Root:          root,
		AllowDegraded: allowDegraded,
	}.Run(context.Background(), &contractv1.Manifest{Slug: "shop", Apps: []*contractv1.ManifestApp{{Name: "web"}}})
	return verdicts["web"], err
}
