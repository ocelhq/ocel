package vps_test

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit"
	vps "github.com/ocelhq/ocel/platform/vps/provider"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
	vars "github.com/ocelhq/ocel/platform/vps/provider/live"
)

const probedStore = "?cors"

func aNamedStore() vps.ExternalStore {
	return vps.ExternalStore{
		Endpoint: "https://s3.example.com", Region: "eu-west-1", Bucket: "shared",
		AccessKeyID: "AKIA", SecretAccessKey: "elsewhere", PathStyle: true,
	}
}

func saidBy(answers map[string]string) string {
	said := strings.Builder{}
	for name, code := range answers {
		said.WriteString(name + "=" + code + "\n")
	}
	for name, body := range map[string]string{
		"cors-get":         `<CORSConfiguration><CORSRule><AllowedOrigin>https://probe.ocel.invalid</AllowedOrigin></CORSRule></CORSConfiguration>`,
		"presigned-get":    "ocel",
		"multipart-create": "<UploadId>an-upload</UploadId>",
	} {
		said.WriteString(name + ".body=" + base64.StdEncoding.EncodeToString([]byte(body)) + "\n")
	}
	return said.String()
}

func aStoreServingEverything() map[string]string {
	return map[string]string{
		"cors-before": "404", "cors-put": "200", "cors-get": "200",
		"conditional-put": "200", "conditional-again": "412",
		"presigned-put": "200", "presigned-get": "200",
		"post-policy": "204", "multipart-create": "200",
	}
}

func probing(machine *scripted, store vps.ExternalStore) (*vps.Provider, error) {
	p := vps.ProviderOver(
		vps.Options{SSH: vps.Target{Host: "box.invalid", User: "ada"}, Bucket: store},
		func(context.Context) (host.Conn, error) { return machine, nil },
	)
	stack, err := naming.ParseStackName("prod--web--r0a1b2c3d")
	if err != nil {
		return nil, err
	}
	return p, p.PreflightDeploy(context.Background(), providerkit.DeployPreflight{
		Plan: providerkit.DeployPlan{
			Slug:  "shop",
			Class: providerkit.ClassProduction,
			Apps:  []providerkit.AppEntry{{App: "web", Stack: stack, Image: deployedRef}},
		},
		Resources: []providerkit.Resource{{
			Name: "uploads", Type: providerkit.BindingBucket, Bucket: &providerkit.BucketSpec{},
		}},
	})
}

func TestAStoreThatServesNoneOfWhatABucketNeedsIsRefusedBeforeAnythingIsProvisioned(t *testing.T) {
	t.Parallel()

	machine := boxSaying(map[string]answer{probedStore: {stdout: ""}})
	_, err := probing(machine, aNamedStore())
	if err == nil {
		t.Fatal("PreflightDeploy() let a deploy past a store that answers none of the calls a bucket is served by")
	}
	for what, named := range map[string]string{
		"the bucket the project was pointed at": "shared",
		"a write it must refuse twice":          "If-None-Match",
		"the origins a browser reaches it from": "Cors",
		"the urls every byte rides":             "presigned url",
		"the uploads larger than one request":   "multipart",
	} {
		if !strings.Contains(err.Error(), named) {
			t.Errorf("the refusal names nothing of %s:\n%s", what, err)
		}
	}
	for _, command := range machine.ran {
		if strings.Contains(command, "docker run") {
			t.Errorf("a refused store still had something stood up:\n%s", command)
		}
	}
}

func TestAStoreMissingOnlyItsPostPolicyIsDeployedOntoWithPutsInstead(t *testing.T) {
	t.Parallel()

	answers := aStoreServingEverything()
	answers["post-policy"] = "400"
	machine := boxSaying(map[string]answer{probedStore: {stdout: saidBy(answers)}})

	p, err := probing(machine, aNamedStore())
	if err != nil {
		t.Fatalf("PreflightDeploy() = %v, want a store that signs no post policy to be deployed onto all the same", err)
	}
	if manifest := storeSectionOf(t, p, machine); manifest.Store.PostPolicies {
		t.Error("the runtime is told the store signs post policies, and a browser would be handed a form the store refuses")
	}
}

func TestAStoreThatSignsAPostPolicyHasItRecordedWhereTheRuntimeReadsIt(t *testing.T) {
	t.Parallel()

	machine := boxSaying(map[string]answer{probedStore: {stdout: saidBy(aStoreServingEverything())}})

	p, err := probing(machine, aNamedStore())
	if err != nil {
		t.Fatalf("PreflightDeploy() = %v, want a store that serves every call to be let through", err)
	}
	if manifest := storeSectionOf(t, p, machine); !manifest.Store.PostPolicies {
		t.Error("the runtime is left to guess at the post policy the probe found, and a browser upload would go up unbounded")
	}
}

func storeSectionOf(t *testing.T, p *vps.Provider, machine *scripted) vars.Manifest {
	t.Helper()
	app := anApp()
	app.Values = providerkit.AppValues{Bindings: []providerkit.Binding{bindingBucket()}}
	if _, err := p.ProvisionContainers(context.Background(), aStack(t, app), nil); err != nil {
		t.Fatalf("ProvisionContainers() = %v", err)
	}
	manifest := manifestFed(t, machine.carried())
	if manifest.Store == nil {
		t.Fatal("the app binding a bucket was handed no store")
	}
	return manifest
}
