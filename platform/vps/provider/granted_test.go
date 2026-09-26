package vps_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/constants"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	vps "github.com/ocelhq/ocel/platform/vps/provider"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
	vars "github.com/ocelhq/ocel/platform/vps/provider/live"
)

func TestABucketAnswersTheHostnamesItsOwnProjectClaims(t *testing.T) {
	t.Parallel()

	machine := &box{}
	written, err := host.WriteRoutingTable(host.RoutingTable{
		Grace: host.DrainWindow,
		Claims: []host.HostClaim{{
			Hostname: "shop.example.com", Owner: vars.Surface("shop", "production"),
			Pointer: "@production", App: "web",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	machine.routingDoc = string(written)

	if _, err := over(machine).ProvisionBucket(context.Background(), aBucket(t, "uploads", false), nil); err != nil {
		t.Fatalf("Bucket() = %v", err)
	}

	held := fedBy(t, machine, "?cors")
	if !strings.Contains(held, "<AllowedOrigin>https://shop.example.com</AllowedOrigin>") {
		t.Errorf("the bucket does not answer the hostname its own project claims, so a browser on it is refused:\n%s", held)
	}
	if strings.Contains(held, "<AllowedOrigin>*</AllowedOrigin>") {
		t.Errorf("the bucket answers every origin on the internet:\n%s", held)
	}
}

func grantedBucket() provider.Binding {
	return provider.Binding{
		Type: provider.BindingBucket, Name: "shared", Resource: "shared",
		Properties: map[string]string{provider.PropertyBucket: "prod-other-r9z8y7x6w-shared"},
	}
}

func manifestFor(t *testing.T, machine *box, options vps.Options, app provider.AppSpec) vars.Manifest {
	t.Helper()
	p := vps.ProviderOver(options, func(context.Context) (host.Conn, error) { return machine, nil })
	if _, err := p.ProvisionContainers(context.Background(), aStack(t, app), nil); err != nil {
		t.Fatalf("ProvisionContainers() = %v", err)
	}
	return manifestIn(t, machine)
}

func boundApp() provider.AppSpec {
	app := anApp()
	app.Values = provider.AppValues{Bindings: []provider.Binding{bindingBucket()}}
	app.Grants = []provider.Binding{grantedBucket()}
	return app
}

func TestTheRuntimeIsHandedTheExactSetOfBucketsTheDeployGranted(t *testing.T) {
	t.Parallel()

	manifest := manifestFor(t, &box{kept: sealedRootKey()},
		vps.Options{SSH: vps.Target{Host: "box.invalid", User: "ada"}}, boundApp())
	if manifest.Store == nil {
		t.Fatal("an app binding a bucket was handed no store")
	}
	want := []string{"prod-web-r0a1b2c3d-uploads", "prod-other-r9z8y7x6w-shared"}
	if !slices.Equal(manifest.Store.Granted, want) {
		t.Errorf("the runtime is handed %v, want %v: a service that cannot name what it was granted has to trust what the caller names",
			manifest.Store.Granted, want)
	}
}

func TestAnAppsUploadSessionsLiveWhereNoOtherAppsAccountReaches(t *testing.T) {
	t.Parallel()

	options := vps.Options{SSH: vps.Target{Host: "box.invalid", User: "ada"}}
	web := manifestFor(t, &box{kept: sealedRootKey()}, options, boundApp())

	other := boundApp()
	other.App = "admin"
	admin := manifestFor(t, &box{kept: sealedRootKey()}, options, other)

	if web.Store.Sessions == admin.Store.Sessions {
		t.Fatalf("both apps keep their upload sessions at %q, so either reads the other's session secrets and forges its callbacks",
			web.Store.Sessions)
	}
	for _, held := range []*vars.Store{web.Store, admin.Store} {
		if !strings.HasPrefix(held.Sessions, constants.StoreSessionsBucket()+"/") {
			t.Errorf("upload sessions live at %q, want a prefix inside the store's own sessions bucket", held.Sessions)
		}
		if slices.Contains(held.Granted, held.Sessions) {
			t.Errorf("the sessions prefix %q is one of the granted buckets, so a request can name it", held.Sessions)
		}
	}
}

func TestAnAppsStoreAccountIsHeldToItsOwnSessions(t *testing.T) {
	t.Parallel()

	machine := &box{kept: sealedRootKey()}
	manifest := manifestFor(t, machine, vps.Options{SSH: vps.Target{Host: "box.invalid", User: "ada"}}, boundApp())

	policy := fedBy(t, machine, "add-service-account")
	if !strings.Contains(policy, "arn:aws:s3:::"+manifest.Store.Sessions+"/*") {
		t.Errorf("the app's account is not held to its own sessions prefix:\n%s", policy)
	}
	if strings.Contains(policy, `"arn:aws:s3:::`+constants.StoreSessionsBucket()+`"`) ||
		strings.Contains(policy, `"arn:aws:s3:::`+constants.StoreSessionsBucket()+`/*"`) {
		t.Errorf("the app's account reaches every app's upload sessions:\n%s", policy)
	}
}
