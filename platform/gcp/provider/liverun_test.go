package gcp_test

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/tarball"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit"
	gcp "github.com/ocelhq/ocel/platform/gcp/provider"
)

var liveRelease = naming.NewRelease("live", "wp4")

var nodeRuntime = providerkit.Runtime{Name: providerkit.RuntimeNode, Arch: providerkit.ArchX8664}

func runnable(t *testing.T) *gcp.Provider {
	t.Helper()
	if !emulated() {
		t.Skip("a Cloud Run release runs containers, and only the emulator runs them out of a daemon this test can write to")
	}
	return live(t)
}

func reachable(t *testing.T, uri string) string {
	t.Helper()
	if !emulated() {
		return uri
	}
	at, err := url.Parse(uri)
	if err != nil {
		t.Fatalf("parse the url Cloud Run answered with (%q): %v", uri, err)
	}
	emulator, err := url.Parse(endpoint())
	if err != nil {
		t.Fatal(err)
	}
	at.Host = at.Hostname() + ":" + emulator.Port()
	return at.String()
}

func answering(t *testing.T, uri string) (int, string) {
	t.Helper()
	var status int
	var body string
	deadline := time.Now().Add(90 * time.Second)
	for {
		status, body = asked(t, uri)
		if status == http.StatusOK || time.Now().After(deadline) {
			return status, body
		}
		time.Sleep(time.Second)
	}
}

func gone(t *testing.T, uri string) (int, string) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for {
		status, said := asked(t, uri)
		if status != http.StatusOK || time.Now().After(deadline) {
			return status, said
		}
		time.Sleep(time.Second)
	}
}

func asked(t *testing.T, uri string) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, uri, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()
	said, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	return resp.StatusCode, strings.TrimSpace(string(said))
}

func stagedFunction(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	config, err := json.Marshal(providerkit.FunctionConfig{
		Runtime: nodeRuntime, Handler: "index.mjs", ID: "live", App: "live",
	})
	if err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"index.mjs": body, providerkit.FunctionConfigFile: string(config)} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func held(t *testing.T, repository string, image v1.Image) string {
	t.Helper()
	digest, err := image.Digest()
	if err != nil {
		t.Fatal(err)
	}
	target := repository + ":" + naming.DigestTag(digest.String())
	push := providerkit.ImagePush{App: repository, Target: target, Digest: digest.String(), Built: image}
	if err := providerkit.DaemonImages().Push(context.Background(), push, nil); err != nil {
		t.Fatalf("write %s into the docker daemon the emulator runs out of: %v", target, err)
	}
	return strings.TrimSuffix(target, ":"+naming.DigestTag(digest.String())) + "@" + digest.String()
}

func functionImage(t *testing.T, p *gcp.Provider, repository, body string) string {
	t.Helper()
	ctx := context.Background()
	base, err := p.FunctionBase(ctx, nodeRuntime)
	if err != nil {
		t.Fatalf("read the base a node function is built on: %v", err)
	}
	membrane, err := p.FunctionMembrane(ctx, nodeRuntime)
	if err != nil {
		t.Fatal(err)
	}
	image, err := providerkit.FunctionImage(base, nodeRuntime, stagedFunction(t, body),
		map[string][]byte{providerkit.NodeMembranePath: membrane})
	if err != nil {
		t.Fatalf("build the function's image: %v", err)
	}
	return held(t, repository, image)
}

func serverImage(t *testing.T, p *gcp.Provider, repository, mark string) string {
	t.Helper()
	base, err := p.FunctionBase(context.Background(), nodeRuntime)
	if err != nil {
		t.Fatal(err)
	}
	body := "import http from 'node:http';" +
		"http.createServer((q,r)=>{r.end('" + mark + "')}).listen(Number(process.env.PORT),'0.0.0.0');"
	layer, err := tarball.LayerFromOpener(func() (io.ReadCloser, error) {
		var packed bytes.Buffer
		archive := tar.NewWriter(&packed)
		if err := archive.WriteHeader(&tar.Header{
			Typeflag: tar.TypeReg, Name: "srv/server.mjs", Mode: 0o644, Size: int64(len(body)),
		}); err != nil {
			return nil, err
		}
		if _, err := archive.Write([]byte(body)); err != nil {
			return nil, err
		}
		if err := archive.Close(); err != nil {
			return nil, err
		}
		return io.NopCloser(bytes.NewReader(packed.Bytes())), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	appended, err := mutate.Append(base, mutate.Addendum{Layer: layer})
	if err != nil {
		t.Fatal(err)
	}
	file, err := appended.ConfigFile()
	if err != nil {
		t.Fatal(err)
	}
	config := file.Config
	config.Cmd = []string{"node", "/srv/server.mjs"}
	image, err := mutate.Config(appended, config)
	if err != nil {
		t.Fatal(err)
	}
	return held(t, repository, image)
}

func serverlessPlan(app, image string, values map[string]string) providerkit.StackPlan {
	return providerkit.StackPlan{
		Ref: providerkit.StackRef{
			Project: "live",
			Class:   providerkit.ClassPreview,
			Name:    naming.AppStack(providerkit.ProductionEnv, app, liveRelease),
		},
		Kind: providerkit.StackApp,
		App: &providerkit.AppPlan{
			App:     app,
			Compute: providerkit.ComputeServerless,
			Values:  providerkit.AppValues{Delivered: values},
			Functions: []providerkit.FunctionSpec{
				{Name: app, Runtime: nodeRuntime, Image: image, URL: true},
			},
		},
	}
}

func containerPlan(app, image string, values map[string]string) providerkit.StackPlan {
	plan := serverlessPlan(app, image, values)
	plan.App.Compute = providerkit.ComputeContainer
	plan.App.Functions = nil
	plan.App.Image = image
	plan.App.HealthCheckPath = "/"
	return plan
}

func TestLiveAFunctionImageBecomesAServiceThatAnswers(t *testing.T) {
	ctx := context.Background()
	p := runnable(t)
	image := functionImage(t, p, "ocel-live/fn", "export default { fetch: () => new Response(process.env.MARK) };")
	plan := serverlessPlan("fn", image, map[string]string{"MARK": "membrane-one"})
	t.Cleanup(func() { _ = p.RemoveFunctions(ctx, plan.Ref, runningAs(t, p, plan), nil) })

	functions, err := p.ProvisionFunctions(ctx, plan, nil)
	if err != nil {
		t.Fatalf("ProvisionFunctions() = %v", err)
	}
	if len(functions) != 1 || functions[0].URL == "" {
		t.Fatalf("ProvisionFunctions() = %+v, want one function reachable at a url of its own", functions)
	}
	if functions[0].Physical == "" {
		t.Error("the function names no service, so nothing could take it down again")
	}

	status, said := answering(t, reachable(t, functions[0].URL))
	if status != http.StatusOK {
		t.Fatalf("GET %s = %d %q, want the function to answer", functions[0].URL, status, said)
	}
	if said != "membrane-one" {
		t.Errorf("GET %s said %q, want the value the deploy delivered, served through the membrane", functions[0].URL, said)
	}
}

func runningAs(t *testing.T, p *gcp.Provider, plan providerkit.StackPlan) []providerkit.Function {
	t.Helper()
	service, err := p.Names().Service(plan.Ref.Project, plan.Ref.Name.Env, plan.App.App, plan.App.App)
	if err != nil {
		t.Fatal(err)
	}
	return []providerkit.Function{{Name: plan.App.App, Physical: service}}
}

func TestLiveAContainerAppIsStoodUpAndReleasedAgainOntoANewRevision(t *testing.T) {
	ctx := context.Background()
	p := runnable(t)
	first := serverImage(t, p, "ocel-live/app", "served-one")
	plan := containerPlan("app", first, nil)
	t.Cleanup(func() {
		_ = p.RemoveContainers(ctx, plan.Ref, []providerkit.AppContainer{
			{Name: "app", Physical: runningAs(t, p, plan)[0].Physical},
		}, nil)
	})

	containers, err := p.ProvisionContainers(ctx, plan, nil)
	if err != nil {
		t.Fatalf("ProvisionContainers() = %v", err)
	}
	if len(containers) != 1 || containers[0].URL == "" {
		t.Fatalf("ProvisionContainers() = %+v, want one container reachable at a url of its own", containers)
	}
	at := reachable(t, containers[0].URL)
	if status, said := answering(t, at); status != http.StatusOK || said != "served-one" {
		t.Fatalf("GET %s = %d %q, want the image this release stood up", at, status, said)
	}

	second := serverImage(t, p, "ocel-live/app", "served-two")
	if second == first {
		t.Fatal("both releases carry one image, so nothing would prove a new revision serves")
	}
	released, err := p.ProvisionContainers(ctx, containerPlan("app", second, nil), nil)
	if err != nil {
		t.Fatalf("ProvisionContainers() a second time = %v", err)
	}
	if released[0].URL != containers[0].URL {
		t.Errorf("the app moved from %s to %s, and a release keeps the url it was reached at", containers[0].URL, released[0].URL)
	}
	if status, said := answering(t, at); status != http.StatusOK || said != "served-two" {
		t.Errorf("GET %s = %d %q, want the image the second release stood up: traffic is pinned to the revision it made", at, status, said)
	}
}

func TestLiveAServiceTakenDownAnswersNothingAndIsTakenDownOnlyOnce(t *testing.T) {
	ctx := context.Background()
	p := runnable(t)
	image := serverImage(t, p, "ocel-live/gone", "still-here")
	plan := containerPlan("gone", image, nil)

	containers, err := p.ProvisionContainers(ctx, plan, nil)
	if err != nil {
		t.Fatalf("ProvisionContainers() = %v", err)
	}
	at := reachable(t, containers[0].URL)
	if status, said := answering(t, at); status != http.StatusOK {
		t.Fatalf("GET %s = %d %q, want it up before it is taken down", at, status, said)
	}

	if err := p.RemoveContainers(ctx, plan.Ref, containers, nil); err != nil {
		t.Fatalf("RemoveContainers() = %v", err)
	}
	if status, said := gone(t, at); status == http.StatusOK {
		t.Errorf("GET %s = %d %q after the service was taken down, want nothing answering there", at, status, said)
	}
	if err := p.RemoveContainers(ctx, plan.Ref, containers, nil); err != nil {
		t.Errorf("RemoveContainers() a second time = %v, want a teardown that is safe to re-run", err)
	}
}
