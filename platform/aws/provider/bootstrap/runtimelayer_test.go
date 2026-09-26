package bootstrap

import (
	"context"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/ocelhq/ocel/pkg/providerkit/arch"
	"github.com/ocelhq/ocel/platform/aws/provider/cfn"
	"github.com/ocelhq/ocel/platform/aws/provider/payloads"
)

type runtimeLayerTemplateShape struct {
	Resources map[string]struct {
		Type       string `yaml:"Type"`
		Properties struct {
			LayerName string `yaml:"LayerName"`
			Content   struct {
				S3Bucket string `yaml:"S3Bucket"`
				S3Key    string `yaml:"S3Key"`
			} `yaml:"Content"`
			CompatibleArchitectures []string `yaml:"CompatibleArchitectures"`
			CompatibleRuntimes      []string `yaml:"CompatibleRuntimes"`
		} `yaml:"Properties"`
	} `yaml:"Resources"`
	Outputs map[string]struct {
		Value string `yaml:"Value"`
	} `yaml:"Outputs"`
}

func parseRuntimeLayerTemplate(t *testing.T, body string) runtimeLayerTemplateShape {
	t.Helper()
	var tmpl runtimeLayerTemplateShape
	if err := yaml.Unmarshal([]byte(body), &tmpl); err != nil {
		t.Fatalf("the runtime layer template is not valid YAML: %v", err)
	}
	return tmpl
}

func runtimeLayerBody(t *testing.T, class string) string {
	t.Helper()
	body, err := runtimeLayerTemplateAt(defaultNamespace, class, fixtureBucket)
	if err != nil {
		t.Fatalf("runtimeLayerTemplateAt: %v", err)
	}
	return body
}

func TestRuntimeLayerTemplate(t *testing.T) {
	t.Run("one layer version per architecture, named after the payload it contains", func(t *testing.T) {
		for _, class := range []string{ClassProduction, ClassPreview} {
			t.Run(class, func(t *testing.T) {
				tmpl := parseRuntimeLayerTemplate(t, runtimeLayerBody(t, class))
				if len(tmpl.Resources) != len(runtimeArches()) {
					t.Fatalf("the runtime stack declares %d resources, want one layer version per architecture (%d)",
						len(tmpl.Resources), len(runtimeArches()))
				}
				for _, architecture := range runtimeArches() {
					shipped, err := payloads.RuntimeLayer(architecture)
					if err != nil {
						t.Fatalf("payloads.RuntimeLayer(%s): %v", architecture, err)
					}
					layer, ok := tmpl.Resources[runtimeLayerResourceID(architecture)]
					if !ok {
						t.Fatalf("the runtime stack declares no %s layer", architecture)
					}
					if layer.Type != "AWS::Lambda::LayerVersion" {
						t.Errorf("%s Type = %q, want AWS::Lambda::LayerVersion", architecture, layer.Type)
					}
					if !strings.Contains(layer.Properties.LayerName, shortRuntimeDigest(shipped.SHA256)) {
						t.Errorf("%s LayerName = %q, want the payload digest %s in it",
							architecture, layer.Properties.LayerName, shortRuntimeDigest(shipped.SHA256))
					}
					if !strings.Contains(layer.Properties.LayerName, runtimeArchTokens[architecture]) {
						t.Errorf("%s LayerName = %q, names no architecture, so the two arches would collide",
							architecture, layer.Properties.LayerName)
					}
					if want := payloads.Key(runtimeLayerKeyPrefix, shipped.SHA256); layer.Properties.Content.S3Key != want {
						t.Errorf("%s S3Key = %q, want %q", architecture, layer.Properties.Content.S3Key, want)
					}
					if layer.Properties.Content.S3Bucket != fixtureBucket {
						t.Errorf("%s S3Bucket = %q, want the bootstrap's artifact bucket %q", architecture, layer.Properties.Content.S3Bucket, fixtureBucket)
					}
					if len(layer.Properties.CompatibleArchitectures) != 1 || layer.Properties.CompatibleArchitectures[0] != architecture {
						t.Errorf("%s CompatibleArchitectures = %v, want [%s]", architecture, layer.Properties.CompatibleArchitectures, architecture)
					}
					if len(layer.Properties.CompatibleRuntimes) != 0 {
						t.Errorf("%s CompatibleRuntimes = %v, want none listed: Lambda refuses the layer to any function whose runtime is left off the list, and one runtime serves them all",
							architecture, layer.Properties.CompatibleRuntimes)
					}
					out, ok := tmpl.Outputs[runtimeLayerOutputKey(architecture, shipped.SHA256)]
					if !ok {
						t.Fatalf("the runtime stack publishes no %s output, so no release can reach the layer", runtimeLayerOutputKey(architecture, shipped.SHA256))
					}
					if !strings.Contains(out.Value, runtimeLayerResourceID(architecture)) {
						t.Errorf("%s output = %q, want the version ARN of %s", architecture, out.Value, runtimeLayerResourceID(architecture))
					}
				}
			})
		}
	})

	t.Run("production and preview name different layers", func(t *testing.T) {
		production := parseRuntimeLayerTemplate(t, runtimeLayerBody(t, ClassProduction))
		preview := parseRuntimeLayerTemplate(t, runtimeLayerBody(t, ClassPreview))
		for _, architecture := range runtimeArches() {
			id := runtimeLayerResourceID(architecture)
			if production.Resources[id].Properties.LayerName == preview.Resources[id].Properties.LayerName {
				t.Errorf("both classes publish the %s runtime as %q, so one class's bootstrap writes the other's layer",
					architecture, production.Resources[id].Properties.LayerName)
			}
		}
	})
}

func TestRunRuntimeLayers(t *testing.T) {
	t.Run("a first bootstrap places both payloads and publishes the layers", func(t *testing.T) {
		stacks, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
		store := newFakeObjectStore()
		frontedBy(t, &fakeEdge{kind: "cloudflare"})

		if err := runAll(context.Background(), apisOf(stacks, ssmc, iamc, store), productionBootstrap(defaultNamespace)); err != nil {
			t.Fatalf("run: %v", err)
		}
		body := stacks.template(runtimeStack(ClassProduction))
		if body == "" {
			t.Fatal("a first bootstrap created no runtime stack, so its releases have nothing to boot through")
		}
		for _, architecture := range runtimeArches() {
			shipped, err := payloads.RuntimeLayer(architecture)
			if err != nil {
				t.Fatalf("payloads.RuntimeLayer(%s): %v", architecture, err)
			}
			key := payloads.Key(runtimeLayerKeyPrefix, shipped.SHA256)
			if !store.has(key) {
				t.Errorf("the %s runtime payload was never placed at %s", architecture, key)
			}
			if !strings.Contains(body, key) {
				t.Errorf("the runtime stack does not point at the placed %s payload %s", architecture, key)
			}
		}
	})

	t.Run("a second bootstrap over the same payloads writes nothing again", func(t *testing.T) {
		stacks, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
		store := preloadedStore()
		frontedBy(t, &fakeEdge{kind: "cloudflare"})

		if err := runAll(context.Background(), apisOf(stacks, ssmc, iamc, store), productionBootstrap(defaultNamespace)); err != nil {
			t.Fatalf("first run: %v", err)
		}
		if store.puts != 0 {
			t.Errorf("placed %d payloads into an account that already stores them", store.puts)
		}
		creates := stacks.creates
		if err := runAll(context.Background(), apisOf(stacks, ssmc, iamc, store), productionBootstrap(defaultNamespace)); err != nil {
			t.Fatalf("second run: %v", err)
		}
		if stacks.creates != creates {
			t.Errorf("the second bootstrap created %d more stacks, want none", stacks.creates-creates)
		}
	})
}

func TestReadRuntimeLayers(t *testing.T) {
	t.Run("the ARNs this build's runtime is published under are read back", func(t *testing.T) {
		stacks := newFakeCFN()
		stacks.seed(coreStackName, "Outputs:\n")
		stacks.seed(runtimeStack(ClassProduction), runtimeLayerBody(t, ClassProduction))

		deployed, err := CheckDeployed(context.Background(), stacks, defaultNamespace)
		if err != nil {
			t.Fatalf("CheckDeployed: %v", err)
		}
		for _, architecture := range runtimeArches() {
			shipped, err := payloads.RuntimeLayer(architecture)
			if err != nil {
				t.Fatalf("payloads.RuntimeLayer(%s): %v", architecture, err)
			}
			want := stacks.output(runtimeStack(ClassProduction), runtimeLayerOutputKey(architecture, shipped.SHA256))
			if want == "" {
				t.Fatalf("the seeded runtime stack published no %s ARN", architecture)
			}
			if got := deployed.RuntimeLayers[architecture]; got != want {
				t.Errorf("RuntimeLayers[%s] = %q, want %q", architecture, got, want)
			}
		}
	})

	t.Run("a runtime published from another build is not read as this one", func(t *testing.T) {
		stacks := newFakeCFN()
		stacks.seed(coreStackName, "Outputs:\n")
		stacks.seed(runtimeStack(ClassProduction), "Outputs:\n  "+
			runtimeLayerOutputKey(arch.X8664, strings.Repeat("a", 64))+":\n    Value: !Ref RuntimeLayerX8664\n")

		deployed, err := CheckDeployed(context.Background(), stacks, defaultNamespace)
		if err != nil {
			t.Fatalf("CheckDeployed: %v", err)
		}
		if arn, found := deployed.RuntimeLayers[arch.X8664]; found {
			t.Errorf("read %q as this build's runtime, and it is another build's", arn)
		}
	})

	t.Run("the runtime stack is stamped and read back as current", func(t *testing.T) {
		stacks, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
		frontedBy(t, &fakeEdge{kind: "cloudflare"})
		if err := runAll(context.Background(), apisOf(stacks, ssmc, iamc, preloadedStore()), productionBootstrap(defaultNamespace)); err != nil {
			t.Fatalf("run: %v", err)
		}
		deployed, err := CheckDeployed(context.Background(), stacks, defaultNamespace)
		if err != nil {
			t.Fatalf("CheckDeployed: %v", err)
		}
		if !deployed.RuntimeStack.Present {
			t.Fatal("the runtime stack was applied and reads back as absent")
		}
		if !deployed.RuntimeStack.Current() {
			t.Errorf("the runtime stack reads back as behind: digest %q, intended %q",
				deployed.RuntimeStack.Digest, deployed.RuntimeStack.Intended)
		}
	})
}

func runtimeLayerDigestFor(t *testing.T, class, bucket string) string {
	t.Helper()
	body, err := runtimeLayerTemplateAt(defaultNamespace, class, bucket)
	if err != nil {
		t.Fatalf("runtimeLayerTemplateAt: %v", err)
	}
	return cfn.TemplateDigest(body)
}

func staleRuntimeBody() string {
	return "AWSTemplateFormatVersion: '2010-09-09'\nOutputs:\n  " +
		runtimeLayerOutputKey(arch.X8664, strings.Repeat("b", 64)) +
		":\n    Value: !Ref RuntimeLayerX8664\n"
}

func ensuringAPIs(stacks *fakeCFN, store ObjectStore) APIs {
	return apisOf(stacks, newFakeSSM(), &fakeIAM{}, store)
}

func ensureRuntimeRequest() RuntimeLayerRequest {
	return RuntimeLayerRequest{ArtifactBucket: fixtureBucket, Writer: "1.4.0"}
}

func assertShipsThisBuild(t *testing.T, stacks *fakeCFN, layers map[string]string) {
	t.Helper()
	for _, architecture := range runtimeArches() {
		shipped, err := payloads.RuntimeLayer(architecture)
		if err != nil {
			t.Fatalf("payloads.RuntimeLayer(%s): %v", architecture, err)
		}
		want := stacks.output(runtimeStack(ClassProduction), runtimeLayerOutputKey(architecture, shipped.SHA256))
		if want == "" {
			t.Fatalf("the runtime stack publishes no %s ARN", architecture)
		}
		if layers[architecture] != want {
			t.Errorf("layers[%s] = %q, want %q", architecture, layers[architecture], want)
		}
	}
}

func TestEnsureRuntimeLayers(t *testing.T) {
	t.Run("an account bootstrapped by an older build has this build's runtime published for it", func(t *testing.T) {
		recordWaits(t)
		stacks, store := newFakeCFN(), newFakeObjectStore()
		stacks.seed(runtimeStack(ClassProduction), staleRuntimeBody())
		var log healLog

		layers, err := EnsureRuntimeLayers(context.Background(), ensuringAPIs(stacks, store), defaultNamespace, ClassProduction, ensureRuntimeRequest(), log.write)
		if err != nil {
			t.Fatalf("EnsureRuntimeLayers: %v", err)
		}
		assertShipsThisBuild(t, stacks, layers)
		for _, architecture := range runtimeArches() {
			shipped, err := payloads.RuntimeLayer(architecture)
			if err != nil {
				t.Fatalf("payloads.RuntimeLayer(%s): %v", architecture, err)
			}
			if !store.has(payloads.Key(runtimeLayerKeyPrefix, shipped.SHA256)) {
				t.Errorf("the %s runtime payload the stack points at was never placed", architecture)
			}
		}
		if !log.says("published this build's runtime") {
			t.Errorf("the deploy said %v, want it to say it published the runtime", log.lines)
		}
	})

	t.Run("an account that already has this build's runtime is written to again by nothing", func(t *testing.T) {
		recordWaits(t)
		stacks, store := newFakeCFN(), preloadedStore()
		stacks.seed(runtimeStack(ClassProduction), runtimeLayerBody(t, ClassProduction))
		creates, updates := stacks.creates, stacks.updates
		var log healLog

		layers, err := EnsureRuntimeLayers(context.Background(), ensuringAPIs(stacks, store), defaultNamespace, ClassProduction, ensureRuntimeRequest(), log.write)
		if err != nil {
			t.Fatalf("EnsureRuntimeLayers: %v", err)
		}
		assertShipsThisBuild(t, stacks, layers)
		if stacks.creates != creates || stacks.updates != updates {
			t.Errorf("wrote the runtime stack %d creates and %d updates over a runtime this build already published",
				stacks.creates-creates, stacks.updates-updates)
		}
		if store.puts != 0 {
			t.Errorf("placed %d payloads the account already stores", store.puts)
		}
	})

	t.Run("a runtime another deploy is publishing is waited out rather than raced", func(t *testing.T) {
		recordWaits(t)
		stacks, store := newFakeCFN(), preloadedStore()
		stacks.seed(runtimeStack(ClassProduction), staleRuntimeBody())
		stacks.busyWriting(runtimeStack(ClassProduction), 4, runtimeLayerBody(t, ClassProduction))
		var log healLog

		layers, err := EnsureRuntimeLayers(context.Background(), ensuringAPIs(stacks, store), defaultNamespace, ClassProduction, ensureRuntimeRequest(), log.write)
		if err != nil {
			t.Fatalf("EnsureRuntimeLayers over a stack another deploy is writing: %v", err)
		}
		assertShipsThisBuild(t, stacks, layers)
		if stacks.updates != 0 {
			t.Errorf("executed %d change sets against a stack another deploy was writing", stacks.updates)
		}
		if !log.says("under another run") {
			t.Errorf("the deploy said %v, want it to say it waited while another run wrote the runtime", log.lines)
		}
	})

	t.Run("a runtime another deploy created first is booted through rather than created twice", func(t *testing.T) {
		recordWaits(t)
		stacks, store := newFakeCFN(), preloadedStore()
		stacks.claimedMidCreate(runtimeStack(ClassProduction), runtimeLayerBody(t, ClassProduction))
		var log healLog

		layers, err := EnsureRuntimeLayers(context.Background(), ensuringAPIs(stacks, store), defaultNamespace, ClassProduction, ensureRuntimeRequest(), log.write)
		if err != nil {
			t.Fatalf("EnsureRuntimeLayers against a runtime another deploy created: %v", err)
		}
		assertShipsThisBuild(t, stacks, layers)
	})

	t.Run("a runtime whose stack stays busy and behind is left for the deploy to refuse", func(t *testing.T) {
		recordWaits(t)
		stacks, store := newFakeCFN(), preloadedStore()
		stacks.seed(runtimeStack(ClassProduction), staleRuntimeBody())
		stacks.busyWriting(runtimeStack(ClassProduction), idleAttempts*4, staleRuntimeBody())
		var log healLog

		layers, err := EnsureRuntimeLayers(context.Background(), ensuringAPIs(stacks, store), defaultNamespace, ClassProduction, ensureRuntimeRequest(), log.write)
		if err != nil {
			t.Fatalf("EnsureRuntimeLayers over a stack that never goes idle: %v", err)
		}
		if len(layers) != 0 {
			t.Errorf("read %v as this build's runtime, and no run published it", layers)
		}
	})
}
