package bootstrap

import (
	"context"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/ocelhq/ocel/pkg/providerkit"
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
	t.Run("one layer version per architecture, named after the payload it carries", func(t *testing.T) {
		for _, class := range []string{ClassProduction, ClassPreview} {
			t.Run(class, func(t *testing.T) {
				tmpl := parseRuntimeLayerTemplate(t, runtimeLayerBody(t, class))
				if len(tmpl.Resources) != len(runtimeArches()) {
					t.Fatalf("the runtime stack declares %d resources, want one layer version per architecture (%d)",
						len(tmpl.Resources), len(runtimeArches()))
				}
				for _, arch := range runtimeArches() {
					carried, err := payloads.RuntimeLayer(arch)
					if err != nil {
						t.Fatalf("payloads.RuntimeLayer(%s): %v", arch, err)
					}
					layer, ok := tmpl.Resources[runtimeLayerResourceID(arch)]
					if !ok {
						t.Fatalf("the runtime stack declares no %s layer", arch)
					}
					if layer.Type != "AWS::Lambda::LayerVersion" {
						t.Errorf("%s Type = %q, want AWS::Lambda::LayerVersion", arch, layer.Type)
					}
					if !strings.Contains(layer.Properties.LayerName, shortRuntimeDigest(carried.SHA256)) {
						t.Errorf("%s LayerName = %q, want the payload digest %s in it",
							arch, layer.Properties.LayerName, shortRuntimeDigest(carried.SHA256))
					}
					if !strings.Contains(layer.Properties.LayerName, runtimeArchTokens[arch]) {
						t.Errorf("%s LayerName = %q, names no architecture, so the two arches would collide",
							arch, layer.Properties.LayerName)
					}
					if want := payloads.Key(runtimeLayerKeyPrefix, carried.SHA256); layer.Properties.Content.S3Key != want {
						t.Errorf("%s S3Key = %q, want %q", arch, layer.Properties.Content.S3Key, want)
					}
					if layer.Properties.Content.S3Bucket != fixtureBucket {
						t.Errorf("%s S3Bucket = %q, want the bootstrap's artifact bucket %q", arch, layer.Properties.Content.S3Bucket, fixtureBucket)
					}
					if len(layer.Properties.CompatibleArchitectures) != 1 || layer.Properties.CompatibleArchitectures[0] != arch {
						t.Errorf("%s CompatibleArchitectures = %v, want [%s]", arch, layer.Properties.CompatibleArchitectures, arch)
					}
					if len(layer.Properties.CompatibleRuntimes) != 0 {
						t.Errorf("%s CompatibleRuntimes = %v, want none listed: Lambda refuses the layer to any function whose runtime is left off the list, and one runtime serves them all",
							arch, layer.Properties.CompatibleRuntimes)
					}
					out, ok := tmpl.Outputs[runtimeLayerOutputKey(arch, carried.SHA256)]
					if !ok {
						t.Fatalf("the runtime stack publishes no %s output, so no release can reach the layer", runtimeLayerOutputKey(arch, carried.SHA256))
					}
					if !strings.Contains(out.Value, runtimeLayerResourceID(arch)) {
						t.Errorf("%s output = %q, want the version ARN of %s", arch, out.Value, runtimeLayerResourceID(arch))
					}
				}
			})
		}
	})

	t.Run("production and preview name different layers", func(t *testing.T) {
		production := parseRuntimeLayerTemplate(t, runtimeLayerBody(t, ClassProduction))
		preview := parseRuntimeLayerTemplate(t, runtimeLayerBody(t, ClassPreview))
		for _, arch := range runtimeArches() {
			id := runtimeLayerResourceID(arch)
			if production.Resources[id].Properties.LayerName == preview.Resources[id].Properties.LayerName {
				t.Errorf("both classes publish the %s runtime as %q, so one class's bootstrap writes the other's layer",
					arch, production.Resources[id].Properties.LayerName)
			}
		}
	})
}

func TestRunRuntimeLayers(t *testing.T) {
	t.Run("a first bootstrap places both payloads and stands the layers up", func(t *testing.T) {
		cfn, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
		store := newFakeObjectStore()
		frontedBy(t, &fakeEdge{kind: "cloudflare"})

		if err := runAll(context.Background(), apisOf(cfn, ssmc, iamc, store), productionBootstrap(defaultNamespace)); err != nil {
			t.Fatalf("run: %v", err)
		}
		body := cfn.template(runtimeStack(ClassProduction))
		if body == "" {
			t.Fatal("a first bootstrap stood up no runtime stack, so its releases have nothing to boot through")
		}
		for _, arch := range runtimeArches() {
			carried, err := payloads.RuntimeLayer(arch)
			if err != nil {
				t.Fatalf("payloads.RuntimeLayer(%s): %v", arch, err)
			}
			key := payloads.Key(runtimeLayerKeyPrefix, carried.SHA256)
			if !store.holds(key) {
				t.Errorf("the %s runtime payload was never placed at %s", arch, key)
			}
			if !strings.Contains(body, key) {
				t.Errorf("the runtime stack does not point at the placed %s payload %s", arch, key)
			}
		}
	})

	t.Run("a second bootstrap over the same payloads writes nothing again", func(t *testing.T) {
		cfn, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
		store := preloadedStore()
		frontedBy(t, &fakeEdge{kind: "cloudflare"})

		if err := runAll(context.Background(), apisOf(cfn, ssmc, iamc, store), productionBootstrap(defaultNamespace)); err != nil {
			t.Fatalf("first run: %v", err)
		}
		if store.puts != 0 {
			t.Errorf("placed %d payloads into an account that already holds them", store.puts)
		}
		creates := cfn.creates
		if err := runAll(context.Background(), apisOf(cfn, ssmc, iamc, store), productionBootstrap(defaultNamespace)); err != nil {
			t.Fatalf("second run: %v", err)
		}
		if cfn.creates != creates {
			t.Errorf("the second bootstrap created %d more stacks, want none", cfn.creates-creates)
		}
	})
}

func TestReadRuntimeLayers(t *testing.T) {
	t.Run("the ARNs this build's runtime is published under are read back", func(t *testing.T) {
		cfn := newFakeCFN()
		cfn.seed(coreStackName, "Outputs:\n")
		cfn.seed(runtimeStack(ClassProduction), runtimeLayerBody(t, ClassProduction))

		deployed, err := CheckDeployed(context.Background(), cfn, defaultNamespace)
		if err != nil {
			t.Fatalf("CheckDeployed: %v", err)
		}
		for _, arch := range runtimeArches() {
			carried, err := payloads.RuntimeLayer(arch)
			if err != nil {
				t.Fatalf("payloads.RuntimeLayer(%s): %v", arch, err)
			}
			want := cfn.output(runtimeStack(ClassProduction), runtimeLayerOutputKey(arch, carried.SHA256))
			if want == "" {
				t.Fatalf("the seeded runtime stack published no %s ARN", arch)
			}
			if got := deployed.RuntimeLayers[arch]; got != want {
				t.Errorf("RuntimeLayers[%s] = %q, want %q", arch, got, want)
			}
		}
	})

	t.Run("a runtime published from another build is not read as this one", func(t *testing.T) {
		cfn := newFakeCFN()
		cfn.seed(coreStackName, "Outputs:\n")
		cfn.seed(runtimeStack(ClassProduction), "Outputs:\n  "+
			runtimeLayerOutputKey(providerkit.ArchX8664, strings.Repeat("a", 64))+":\n    Value: !Ref RuntimeLayerX8664\n")

		deployed, err := CheckDeployed(context.Background(), cfn, defaultNamespace)
		if err != nil {
			t.Fatalf("CheckDeployed: %v", err)
		}
		if arn, held := deployed.RuntimeLayers[providerkit.ArchX8664]; held {
			t.Errorf("read %q as this build's runtime, and it carries another build's", arn)
		}
	})

	t.Run("the runtime stack is stamped and read back as current", func(t *testing.T) {
		cfn, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
		frontedBy(t, &fakeEdge{kind: "cloudflare"})
		if err := runAll(context.Background(), apisOf(cfn, ssmc, iamc, preloadedStore()), productionBootstrap(defaultNamespace)); err != nil {
			t.Fatalf("run: %v", err)
		}
		deployed, err := CheckDeployed(context.Background(), cfn, defaultNamespace)
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
	return TemplateDigest(body)
}

func staleRuntimeBody() string {
	return "AWSTemplateFormatVersion: '2010-09-09'\nOutputs:\n  " +
		runtimeLayerOutputKey(providerkit.ArchX8664, strings.Repeat("b", 64)) +
		":\n    Value: !Ref RuntimeLayerX8664\n"
}

func ensuringAPIs(cfn *fakeCFN, store ObjectStore) APIs {
	return apisOf(cfn, newFakeSSM(), &fakeIAM{}, store)
}

func ensureRuntimeRequest() RuntimeLayerRequest {
	return RuntimeLayerRequest{ArtifactBucket: fixtureBucket, Writer: "1.4.0"}
}

func assertCarriesThisBuild(t *testing.T, cfn *fakeCFN, layers map[string]string) {
	t.Helper()
	for _, arch := range runtimeArches() {
		carried, err := payloads.RuntimeLayer(arch)
		if err != nil {
			t.Fatalf("payloads.RuntimeLayer(%s): %v", arch, err)
		}
		want := cfn.output(runtimeStack(ClassProduction), runtimeLayerOutputKey(arch, carried.SHA256))
		if want == "" {
			t.Fatalf("the runtime stack publishes no %s ARN", arch)
		}
		if layers[arch] != want {
			t.Errorf("layers[%s] = %q, want %q", arch, layers[arch], want)
		}
	}
}

func TestEnsureRuntimeLayers(t *testing.T) {
	t.Run("an account bootstrapped by an older build has this build's runtime published for it", func(t *testing.T) {
		holdNothing(t)
		cfn, store := newFakeCFN(), newFakeObjectStore()
		cfn.seed(runtimeStack(ClassProduction), staleRuntimeBody())
		var log healLog

		layers, err := EnsureRuntimeLayers(context.Background(), ensuringAPIs(cfn, store), defaultNamespace, ClassProduction, ensureRuntimeRequest(), log.write)
		if err != nil {
			t.Fatalf("EnsureRuntimeLayers: %v", err)
		}
		assertCarriesThisBuild(t, cfn, layers)
		for _, arch := range runtimeArches() {
			carried, err := payloads.RuntimeLayer(arch)
			if err != nil {
				t.Fatalf("payloads.RuntimeLayer(%s): %v", arch, err)
			}
			if !store.holds(payloads.Key(runtimeLayerKeyPrefix, carried.SHA256)) {
				t.Errorf("the %s runtime payload the stack points at was never placed", arch)
			}
		}
		if !log.says("published this build's runtime") {
			t.Errorf("the deploy said %v, want it to say it published the runtime", log.lines)
		}
	})

	t.Run("an account already carrying this build's runtime is written to again by nothing", func(t *testing.T) {
		holdNothing(t)
		cfn, store := newFakeCFN(), preloadedStore()
		cfn.seed(runtimeStack(ClassProduction), runtimeLayerBody(t, ClassProduction))
		creates, updates := cfn.creates, cfn.updates
		var log healLog

		layers, err := EnsureRuntimeLayers(context.Background(), ensuringAPIs(cfn, store), defaultNamespace, ClassProduction, ensureRuntimeRequest(), log.write)
		if err != nil {
			t.Fatalf("EnsureRuntimeLayers: %v", err)
		}
		assertCarriesThisBuild(t, cfn, layers)
		if cfn.creates != creates || cfn.updates != updates {
			t.Errorf("wrote the runtime stack %d creates and %d updates over a runtime this build already published",
				cfn.creates-creates, cfn.updates-updates)
		}
		if store.puts != 0 {
			t.Errorf("placed %d payloads the account already holds", store.puts)
		}
	})

	t.Run("a runtime another deploy is publishing is waited out rather than raced", func(t *testing.T) {
		holdNothing(t)
		cfn, store := newFakeCFN(), preloadedStore()
		cfn.seed(runtimeStack(ClassProduction), staleRuntimeBody())
		cfn.busyWriting(runtimeStack(ClassProduction), 4, runtimeLayerBody(t, ClassProduction))
		var log healLog

		layers, err := EnsureRuntimeLayers(context.Background(), ensuringAPIs(cfn, store), defaultNamespace, ClassProduction, ensureRuntimeRequest(), log.write)
		if err != nil {
			t.Fatalf("EnsureRuntimeLayers over a stack another deploy holds: %v", err)
		}
		assertCarriesThisBuild(t, cfn, layers)
		if cfn.updates != 0 {
			t.Errorf("executed %d change sets against a stack another deploy was writing", cfn.updates)
		}
		if !log.says("under another run") {
			t.Errorf("the deploy said %v, want it to say it stood by while another run wrote the runtime", log.lines)
		}
	})

	t.Run("a runtime another deploy stood up first is booted through rather than created twice", func(t *testing.T) {
		holdNothing(t)
		cfn, store := newFakeCFN(), preloadedStore()
		cfn.claimedMidCreate(runtimeStack(ClassProduction), runtimeLayerBody(t, ClassProduction))
		var log healLog

		layers, err := EnsureRuntimeLayers(context.Background(), ensuringAPIs(cfn, store), defaultNamespace, ClassProduction, ensureRuntimeRequest(), log.write)
		if err != nil {
			t.Fatalf("EnsureRuntimeLayers against a runtime another deploy created: %v", err)
		}
		assertCarriesThisBuild(t, cfn, layers)
	})

	t.Run("a runtime that settles still behind is left for the deploy to refuse", func(t *testing.T) {
		holdNothing(t)
		cfn, store := newFakeCFN(), preloadedStore()
		cfn.seed(runtimeStack(ClassProduction), staleRuntimeBody())
		cfn.busyWriting(runtimeStack(ClassProduction), settleAttempts*4, staleRuntimeBody())
		var log healLog

		layers, err := EnsureRuntimeLayers(context.Background(), ensuringAPIs(cfn, store), defaultNamespace, ClassProduction, ensureRuntimeRequest(), log.write)
		if err != nil {
			t.Fatalf("EnsureRuntimeLayers over a stack that never settles: %v", err)
		}
		if len(layers) != 0 {
			t.Errorf("read %v as this build's runtime, and no run published it", layers)
		}
	})
}
