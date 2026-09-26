package providerkit_test

import (
	"archive/tar"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/providerkit/appbuild"
	"github.com/ocelhq/ocel/pkg/providerkit/fake"
	"github.com/ocelhq/ocel/pkg/providerkit/images"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
)

type imaging struct {
	*fake.Provider

	base v1.Image
}

func (i imaging) Hooks() provider.Hooks {
	hooks := i.Provider.Hooks()
	hooks.FunctionImages = &provider.FunctionImageHooks{ResolveBase: i.ResolveBase, ReadRuntime: i.ReadRuntime}
	return hooks
}

func (i imaging) ResolveBase(context.Context, appbuild.Framework) (v1.Image, error) {
	if i.base != nil {
		return i.base, nil
	}
	return empty.Image, nil
}

func (imaging) ReadRuntime(context.Context, appbuild.Framework) ([]byte, error) {
	return []byte("export const runtime = 1"), nil
}

func stagedProject(t *testing.T, apps ...string) {
	t.Helper()
	builtApps(t, apps...)
	for _, app := range apps {
		dir := filepath.Join(appbuild.ArtifactRoot(), filepath.FromSlash(appArtifactPath(app)))
		raw, err := json.Marshal(map[string]any{
			"framework": map[string]string{"name": "node", "arch": "x86_64"},
			"handler":   "index.handler",
			"id":        "server",
			"app":       app,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "config.json"), raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func imagingDeployRequest() *contractv1.DeployRequest {
	req := namingARegistry(deployRequest())
	req.Manifest.Functions[0].Framework = &contractv1.Framework{Name: "node", Arch: "x86_64"}
	return req
}

func imagingServed(t *testing.T) (contractv1connect.ProviderServiceClient, *fake.Provider) {
	t.Helper()
	base := fake.NewProvider(fake.Options{})
	served := servedBy(t, imaging{Provider: base})
	standsBootstrapped(t, served)
	return served, base
}

func TestDeployShipsAFunctionAsAnImageWhereTheProviderTakesItThatWay(t *testing.T) {
	stagedProject(t, "web", "admin")
	served, p := imagingServed(t)

	result, _ := deploy(t, served, imagingDeployRequest())
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	pushed := p.Registry().Pushed()
	if len(pushed) != 1 {
		t.Fatalf("the deploy pushed %v, want the one image the app's function runs", pushed)
	}
	if !strings.HasPrefix(pushed[0].ImageRef, "ghcr.io/acme/") {
		t.Errorf("the push wrote %q, want it under the registry the deploy names", pushed[0].ImageRef)
	}

	for _, ref := range p.Artifacts().(*fake.Artifacts).Keys() {
		if ref.Bucket == provider.StoreFunctions {
			t.Errorf("the deploy uploaded %s to the function store, want the image to be the only thing shipped", ref.Key)
		}
	}

	plans := p.FakeStacks().Plans()
	app := plans[len(plans)-1]
	if len(app.Uploads) != 0 {
		t.Errorf("the app plan carries %d uploads, want none: the function travels as an image", len(app.Uploads))
	}
	if len(app.App.Functions) != 1 {
		t.Fatalf("the app plan carries %d functions, want the one the manifest declares", len(app.App.Functions))
	}
	spec := app.App.Functions[0]
	if spec.Image == "" {
		t.Fatal("the function spec names no image, so the provider has nothing to run it from")
	}
	if !strings.Contains(spec.Image, "@sha256:") {
		t.Errorf("the function spec runs %q, want a digest-pinned ref: a tag can move under a revision that is meant to be fixed", spec.Image)
	}
	if spec.Artifact.Key != "" {
		t.Errorf("the function spec names artifact %q as well as an image, want the image alone", spec.Artifact.Key)
	}
}

func TestAFunctionsImageIsNeverMistakenForTheAppsOwn(t *testing.T) {
	stagedProject(t, "web", "admin")
	served, provider := imagingServed(t)

	req := imagingDeployRequest()
	req.Manifest.Functions[0].LogicalName = "web"

	result, _ := deploy(t, served, req)
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	plans := provider.FakeStacks().Plans()
	app := plans[len(plans)-1]
	if app.App.Image != "" {
		t.Errorf("the app plan runs the image %q, want none: the app is serverless and only its functions travel as images", app.App.Image)
	}
	spec := app.App.Functions[0]
	if !strings.Contains(spec.Image, "@sha256:") {
		t.Errorf("the function spec runs %q, want a digest-pinned ref of its own", spec.Image)
	}
}

func refusedImagedDeploy(t *testing.T, name, value, stood string) string {
	t.Helper()
	stagedProject(t, "web", "admin")
	served, _ := imagingServed(t)

	req := declaring(imagingDeployRequest(), resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, name, value)
	stream, err := served.Deploy(context.Background(), req)
	if err != nil {
		t.Fatalf("Deploy() error = %v", err)
	}
	defer stream.Close()
	refusal := ""
	for stream.Receive() {
		result := stream.Msg().GetResult()
		if result.GetSuccess() {
			t.Fatal(stood)
		}
		if result.GetError() != "" {
			refusal = result.GetError()
		}
	}
	return refusal + connectMessage(stream.Err())
}

func TestAFunctionRunAsAnImageRefusesTheNameThePortIsInjectedUnder(t *testing.T) {
	refusal := refusedImagedDeploy(t, "PORT", "3000",
		"Deploy() stood up an app declaring PORT, want it refused: the image is told which port to bind under that very name")

	for _, want := range []string{"PORT", "web"} {
		if !strings.Contains(refusal, want) {
			t.Errorf("the refusal reads %q and never names %q", refusal, want)
		}
	}
}

func TestAFunctionRunAsAnImageRefusesTheNameItsHandlerIsInjectedUnder(t *testing.T) {
	refusal := refusedImagedDeploy(t, images.HandlerName, "/tmp/theirs.mjs",
		"Deploy() stood up an app declaring OCEL_HANDLER, want it refused: the image tells its runtime which file to serve under that very name")

	for _, want := range []string{images.HandlerName, "web", "runtime"} {
		if !strings.Contains(refusal, want) {
			t.Errorf("the refusal reads %q and never names %q", refusal, want)
		}
	}
}

func TestANodeFunctionsImageCarriesTheRuntimeTheProviderHandsIt(t *testing.T) {
	stagedProject(t, "web", "admin")
	served, provider := imagingServed(t)

	result, _ := deploy(t, served, imagingDeployRequest())
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	pushed := provider.Registry().Pushed()
	if len(pushed) != 1 {
		t.Fatalf("the deploy pushed %v, want the one image the app's function runs", pushed)
	}
	layers, err := pushed[0].Built.Layers()
	if err != nil {
		t.Fatal(err)
	}
	var held []string
	for _, layer := range layers {
		held = append(held, tarNames(t, layer)...)
	}
	want := strings.TrimPrefix(images.NodeRuntimePath, "/")
	if !slices.Contains(held, want) {
		t.Errorf("the image holds %v and nothing at %s, so nothing serves the node function it was built for", held, images.NodeRuntimePath)
	}
}

type imagingWithoutRuntime struct{ imaging }

func (w imagingWithoutRuntime) Hooks() provider.Hooks {
	hooks := w.imaging.Hooks()
	hooks.FunctionImages = &provider.FunctionImageHooks{ResolveBase: w.ResolveBase, ReadRuntime: w.ReadRuntime}
	return hooks
}

func (imagingWithoutRuntime) ReadRuntime(context.Context, appbuild.Framework) ([]byte, error) {
	return nil, nil
}

func TestANodeFunctionIsRefusedWhereTheProviderCarriesNoRuntime(t *testing.T) {
	stagedProject(t, "web", "admin")
	served := servedBy(t, imagingWithoutRuntime{imaging{Provider: fake.NewProvider(fake.Options{})}})
	standsBootstrapped(t, served)

	result, _ := deploy(t, served, imagingDeployRequest())
	if result.GetSuccess() {
		t.Fatal("Deploy() shipped a node function with no runtime in its image, want it refused: the image would run node over a file that is not there")
	}
	for _, want := range []string{"runtime", "node"} {
		if !strings.Contains(result.GetError(), want) {
			t.Errorf("the refusal reads %q and never names %q", result.GetError(), want)
		}
	}
}

func wrappingImagingServed(t *testing.T, runtime []byte) (contractv1connect.ProviderServiceClient, *fake.Provider) {
	t.Helper()
	base := fake.NewProvider(fake.Options{})
	served := servedBy(t, imaging{Provider: base.WrappingContainers("amd64", runtime)})
	standsBootstrapped(t, served)
	return served, base
}

func regularFiles(t *testing.T, image v1.Image) []string {
	t.Helper()
	layers, err := image.Layers()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, layer := range layers {
		body, err := layer.Uncompressed()
		if err != nil {
			t.Fatal(err)
		}
		reader := tar.NewReader(body)
		for {
			header, err := reader.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			if header.Typeflag == tar.TypeReg {
				names = append(names, header.Name)
			}
		}
		body.Close()
	}
	slices.Sort(names)
	return names
}

func TestAFunctionImageIsWrappedInTheRuntimeWhereTheProviderCarriesOne(t *testing.T) {
	stagedProject(t, "web", "admin")
	served, provider := wrappingImagingServed(t, containerRuntimeBytes)

	result, _ := deploy(t, served, imagingDeployRequest())
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	pushed := provider.Registry().Pushed()
	if len(pushed) != 1 || pushed[0].Built == nil {
		t.Fatalf("the deploy pushed %v, want the one wrapped image the app's function runs", pushed)
	}
	config := configOf(t, pushed[0].Built)
	if !slices.Equal(config.Entrypoint, []string{appbuild.ContainerRuntimePath}) {
		t.Errorf("the function's image enters at %v, want the runtime: it is what reads the function's secrets live", config.Entrypoint)
	}
	if !slices.Equal(config.Cmd, []string{"node", images.NodeRuntimePath}) {
		t.Errorf("the runtime runs %v, want the node runtime the function is served through", config.Cmd)
	}
	files := regularFiles(t, pushed[0].Built)
	for _, want := range []string{
		strings.TrimPrefix(images.NodeRuntimePath, "/"),
		strings.TrimPrefix(appbuild.ContainerRuntimePath, "/"),
	} {
		if !slices.Contains(files, want) {
			t.Errorf("the image holds %v and nothing at /%s", files, want)
		}
	}
	if slices.Contains(files, strings.TrimPrefix(images.NodeRuntimeRoot, "/")) {
		t.Errorf("the image holds a file at %s, where the node runtime's directory stands, and a file over a directory cannot be loaded", images.NodeRuntimeRoot)
	}
	if asked := provider.WrappedFor(); !slices.Equal(asked, []string{"amd64"}) {
		t.Errorf("the provider was asked for a runtime built for %v, want the architecture the function is built for", asked)
	}
	digest, err := pushed[0].Built.Digest()
	if err != nil {
		t.Fatal(err)
	}
	plans := provider.FakeStacks().Plans()
	if spec := plans[len(plans)-1].App.Functions[0]; !strings.HasSuffix(spec.Image, "@"+digest.String()) {
		t.Errorf("the function spec runs %q, want it pinned to the wrapped image's digest %s", spec.Image, digest)
	}
}

func TestAWrappedFunctionsCoordinateChangesWithTheRuntimeItIsWrappedIn(t *testing.T) {
	targets := map[string]string{}
	for _, runtime := range []string{"one runtime", "another runtime"} {
		stagedProject(t, "web", "admin")
		served, base := wrappingImagingServed(t, []byte(runtime))

		result, _ := deploy(t, served, imagingDeployRequest())
		if result == nil || !result.GetSuccess() {
			t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
		}
		pushed := base.Registry().Pushed()
		if len(pushed) != 1 {
			t.Fatalf("the deploy pushed %v, want one image", pushed)
		}
		targets[runtime] = pushed[0].ImageRef
	}
	if targets["one runtime"] == targets["another runtime"] {
		t.Errorf("one function wrapped in two runtimes is pushed under %q both times, so a rebuilt runtime would be read as already pushed and never reach the registry", targets["one runtime"])
	}
}

func TestAWrappedFunctionIsHandedItsPlainAndSensitiveValuesAndNoSecretOrRecord(t *testing.T) {
	stagedProject(t, "web", "admin")
	served, base := wrappingImagingServed(t, containerRuntimeBytes)

	req := imagingDeployRequest()
	declaring(req, resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, "REGION", "eu-west-1")
	declaring(req, resourcesv1.VariableClass_VARIABLE_CLASS_SENSITIVE, "API_TOKEN", "sensitive-token")
	declaring(req, resourcesv1.VariableClass_VARIABLE_CLASS_SECRET, "DATABASE_URL", "")
	sealValue(t, base, "DATABASE_URL", "postgres://sealed")

	result, _ := deploy(t, served, req)
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	plans := base.FakeStacks().Plans()
	delivered := plans[len(plans)-1].App.Values.Delivered
	for key, want := range map[string]string{"REGION": "eu-west-1", "API_TOKEN": "sensitive-token"} {
		if delivered[key] != want {
			t.Errorf("the function is handed %s=%q, want %q: the runtime reads a declared value off the environment it boots in", key, delivered[key], want)
		}
	}
	if got, held := delivered["DATABASE_URL"]; held {
		t.Errorf("the function is handed DATABASE_URL=%q, want the runtime inside its image to open the secret: a plaintext in the revision is readable by anyone who may describe the service", got)
	}
	if got, held := delivered[provider.ResourceEnvName(provider.BindingPostgres, "orders")]; held {
		t.Errorf("the function is handed the record %q, want the runtime inside its image to read it", got)
	}
}

func TestAnUnsetSecretIsRefusedByThePlanOfAWrappedFunction(t *testing.T) {
	stagedProject(t, "web", "admin")
	served, _ := wrappingImagingServed(t, containerRuntimeBytes)

	req := declaring(imagingDeployRequest(), resourcesv1.VariableClass_VARIABLE_CLASS_SECRET, "DATABASE_URL", "")
	message, events := refusedPlanOn(t, served, req)

	for _, want := range []string{"web", "DATABASE_URL", "ocel env set"} {
		if !strings.Contains(message, want) {
			t.Errorf("the refusal reads %q and never names %q: the runtime refuses to start on an unset secret, so the plan refuses it first", message, want)
		}
	}
	if entered(t, events, "web") {
		t.Error("the deploy was already standing web up when the unset secret was refused")
	}
}

func tarNames(t *testing.T, layer v1.Layer) []string {
	t.Helper()
	body, err := layer.Uncompressed()
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	var names []string
	reader := tar.NewReader(body)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if header.Typeflag == tar.TypeDir {
			continue
		}
		names = append(names, header.Name)
	}
	slices.Sort(names)
	return names
}

func configOf(t *testing.T, image v1.Image) v1.Config {
	t.Helper()
	file, err := image.ConfigFile()
	if err != nil {
		t.Fatal(err)
	}
	return file.Config
}
