package control

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/compute/containerservice"
	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
)

type scalableTargetsRead struct {
	mu        sync.Mutex
	resources []string
}

func (r *scalableTargetsRead) serve(t *testing.T) aws.Config {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			t.Error(err)
		}
		var in struct{ ResourceIds []string }
		if err := json.Unmarshal(body, &in); err != nil {
			t.Errorf("a request the stub cannot read: %s %s", req.Header.Get("X-Amz-Target"), body)
		}
		r.mu.Lock()
		r.resources = append(r.resources, in.ResourceIds...)
		r.mu.Unlock()
		w.Header().Set("Content-Type", "application/x-amz-json-1.1")
		_, _ = io.WriteString(w, `{"ScalableTargets":[]}`)
	}))
	t.Cleanup(server.Close)
	return aws.Config{
		Region:           "us-east-1",
		BaseEndpoint:     aws.String(server.URL),
		Credentials:      credentials.NewStaticCredentialsProvider("id", "secret", ""),
		RetryMaxAttempts: 1,
	}
}

func TestTheControlAssemblyScalesContainerReleasesWithoutARunner(t *testing.T) {
	t.Parallel()

	read := &scalableTargetsRead{}
	p := NewProvider(Options{Region: "us-east-1"}, read.serve(t), defaultNamespace)
	release, err := provider.ParseRelease("0000000000000000000000000000000c~00000000000c")
	if err != nil {
		t.Fatal(err)
	}
	demoted := provider.DemotedReleases{
		Slug:     "shop",
		Tier:     environment.TierProduction,
		Env:      "prod",
		Previous: []provider.AppRelease{{App: "web", Release: release}},
	}

	if err := p.Hooks().ContainerReleases.ScaleDown(context.Background(), demoted, progress.Discard()); err != nil {
		t.Fatalf("ScaleDown() = %v, want the demoted release's service read and nothing to scale: a rollback through an endpoint needs no runner", err)
	}

	service, err := containerservice.NameAt(string(defaultNamespace), containerservice.Coordinate("shop", naming.AppStack("prod", "web", release.Token())))
	if err != nil {
		t.Fatal(err)
	}
	want := "service/" + awsports.ContainerClusterName(string(defaultNamespace), environment.TierProduction) + "/" + service
	if !slices.Equal(read.resources, []string{want}) {
		t.Errorf("ScaleDown() read the scalable targets of %v, want %s, the demoted release's service", read.resources, want)
	}
}
