package vps_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/resources"
	"github.com/ocelhq/ocel/pkg/router"
	s3store "github.com/ocelhq/ocel/platform/s3"
	vps "github.com/ocelhq/ocel/platform/vps/provider"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/boxstore"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

const recordedRootKey = "9a3b48264c5d6e7f8091a2b3c4d5e6f7"

func sealedRootKey() string {
	return base64.StdEncoding.EncodeToString([]byte(fakeSeal+
		base64.StdEncoding.EncodeToString([]byte(recordedRootKey)))) + "\n"
}

func aBucket(t *testing.T, name string, public bool) resources.ProvisionRequest {
	t.Helper()
	stack, err := naming.ParseStackName("prod--web--r0a1b2c3d")
	if err != nil {
		t.Fatal(err)
	}
	return resources.ProvisionRequest{
		Ref: provider.StackRef{Project: "shop", Tier: environment.TierProduction, Name: stack},
		Resource: provider.Resource{
			Name: name, Type: provider.BindingBucket,
			Bucket: &provider.BucketSpec{
				Public:         public,
				AllowedOrigins: []string{"https://app.example.com"},
			},
		},
	}
}

func TestAProviderOverABoxServesBuckets(t *testing.T) {
	t.Parallel()

	if served := over(&box{}).Facts().Bindings; !slices.Contains(served, provider.BindingBucket) {
		t.Errorf("Serves() = %v, and a project declaring a bucket is refused at deploy on a box", served)
	}
}

func TestADeclaredBucketRunsAStoreOnlyItsProjectReaches(t *testing.T) {
	t.Parallel()

	machine := &box{}
	binding, err := over(machine).ProvisionBucket(context.Background(), aBucket(t, "uploads", false), progress.Discard())
	if err != nil {
		t.Fatalf("Bucket() = %v", err)
	}
	if err := provider.VerifyProperties(binding); err != nil {
		t.Fatalf("the binding that came back is one no app can bind a client to: %v", err)
	}
	if binding.Name != "uploads" || binding.Type != provider.BindingBucket {
		t.Errorf("the binding is %s %q, want the bucket the project declared as uploads", binding.Type, binding.Name)
	}

	runCommand := machine.commands()[machine.at("'docker' 'run'")]
	for _, want := range []string{
		"'--network' 'ocel-production-shop'",
		"ocel.tier=production",
		"ocel.project=shop",
		"'--cap-drop' 'ALL'",
		"@sha256:",
	} {
		if !strings.Contains(runCommand, want) {
			t.Errorf("the store was started without %q:\n%s", want, runCommand)
		}
	}
	for _, published := range []string{"--publish", "'-p'"} {
		if strings.Contains(runCommand, published) {
			t.Errorf("the store was started with %s, so the box's disk is reachable from the internet:\n%s", published, runCommand)
		}
	}
}

func TestTwoProjectsDeclaringBucketsOnOneBoxEachRunAndKeepTheirOwnStore(t *testing.T) {
	t.Parallel()

	shop := aBucket(t, "uploads", false).Ref
	blog := shop
	blog.Project = "blog"
	if vps.StoreName(shop) == vps.StoreName(blog) {
		t.Errorf("shop and blog share the store %q, so one opens the other's kept credential and a teardown of one removes the other's objects",
			vps.StoreName(shop))
	}
}

func TestABindingIsKeyedByTheNameTheAppDeclaredTheResourceUnder(t *testing.T) {
	t.Parallel()

	postgres := aPostgres(t, "17")
	postgres.Resource.Name, postgres.Resource.Declared = "db--main", "main"
	machine := &box{}
	withRecordedPostgres(machine)
	binding, err := over(machine).ProvisionPostgres(context.Background(), postgres, progress.Discard())
	if err != nil {
		t.Fatalf("Postgres() = %v", err)
	}
	if binding.Resource != "main" {
		t.Errorf("the postgres binding names resource %q, so the app reads it off %s rather than off %s",
			binding.Resource, provider.ResourceEnvName(binding.Type, binding.Name), provider.ResourceEnvName(binding.Type, "main"))
	}

	bucket := aBucket(t, "bucket--uploads", false)
	bucket.Resource.Declared = "uploads"
	binding, err = over(&box{kept: sealedRootKey()}).ProvisionBucket(context.Background(), bucket, progress.Discard())
	if err != nil {
		t.Fatalf("Bucket() = %v", err)
	}
	if binding.Resource != "uploads" {
		t.Errorf("the bucket binding names resource %q, so the app reads it off %s rather than off %s",
			binding.Resource, provider.ResourceEnvName(binding.Type, binding.Name), provider.ResourceEnvName(binding.Type, "uploads"))
	}
}

func TestOneStoreServesEveryBucketAProjectDeclares(t *testing.T) {
	t.Parallel()

	machine := &box{}
	p := over(machine)
	first, err := p.ProvisionBucket(context.Background(), aBucket(t, "uploads", false), progress.Discard())
	if err != nil {
		t.Fatalf("Bucket(uploads) = %v", err)
	}
	second, err := p.ProvisionBucket(context.Background(), aBucket(t, "avatars", false), progress.Discard())
	if err != nil {
		t.Fatalf("Bucket(avatars) = %v", err)
	}

	if first.Properties[provider.PropertyBucket] == second.Properties[provider.PropertyBucket] {
		t.Fatalf("both buckets bind to %q, and two declared buckets are two stores of objects",
			first.Properties[provider.PropertyBucket])
	}
	stores := 0
	for _, command := range machine.commands() {
		if strings.Contains(command, "'docker' 'run'") && strings.Contains(command, "rustfs") {
			stores++
		}
	}
	if stores != 1 {
		t.Fatalf("two declared buckets started %d stores, and a box runs one per project and environment", stores)
	}
}

func TestTheStoreRunsOnACredentialTheBoxKeepsSealed(t *testing.T) {
	t.Parallel()

	machine := &box{kept: sealedRootKey()}
	if _, err := over(machine).ProvisionBucket(context.Background(), aBucket(t, "uploads", false), progress.Discard()); err != nil {
		t.Fatalf("Bucket() = %v", err)
	}

	joined := strings.Join(machine.commands(), "\n")
	if !strings.Contains(joined, host.KeptPath(environment.TierProduction, "shop-prod-infra-store-s3")) {
		t.Fatalf("nothing about the store's credential was kept on the box:\n%s", joined)
	}
	for _, fed := range machine.feeds() {
		if strings.Contains(fed, recordedRootKey) && !strings.Contains(fed, "RUSTFS_SECRET_KEY") {
			t.Fatalf("the store's root credential was sent to the box outside the env file it is handed in:\n%s", fed)
		}
	}
	handed := host.EnvFile(environment.TierProduction, "shop-prod-infra-store-s3")
	if !strings.Contains(joined, "rm -f "+quotedPath(handed)) {
		t.Fatalf("the file the store's credential was handed over in is left on the box:\n%s", joined)
	}
}

func quotedPath(path string) string { return "'" + path + "'" }

func sealingStore(t *testing.T, declared ...string) string {
	t.Helper()
	machine := &box{}
	vendor := over(machine)
	for _, name := range declared {
		if _, err := vendor.ProvisionBucket(context.Background(), aBucket(t, name, false), progress.Discard()); err != nil {
			t.Fatalf("Bucket(%s) = %v", name, err)
		}
	}
	for _, command := range machine.commands() {
		if strings.Contains(command, boxstore.SealHelper) {
			return command
		}
	}
	t.Fatal("nothing the deploy ran sealed the store's credential")
	return ""
}

func TestTheStoreCredentialIsSealedUnderTheStoreAndNotWhicheverBucketProvisionedIt(t *testing.T) {
	t.Parallel()

	first := sealingStore(t, "avatars", "uploads")
	second := sealingStore(t, "uploads", "avatars")
	if first != second {
		t.Errorf("the store's credential is sealed at\n%s\nwhen avatars is declared first and at\n%s\nwhen uploads is:"+
			" the associated data is what the seal is bound to, so the next deploy opens nothing", first, second)
	}
}

func TestTheSecretAStoreAccountIsMintedWithIsOneTheStoreWillTake(t *testing.T) {
	t.Parallel()

	secret, err := vps.MintStoreSecret()
	if err != nil {
		t.Fatalf("MintStoreSecret() = %v", err)
	}
	if err := host.CheckStoreSecret(secret); err != nil {
		t.Errorf("a minted store secret is %d characters: %v", len(secret), err)
	}
}

func bindingBucket() provider.Binding {
	return provider.Binding{
		Type: provider.BindingBucket, Name: "uploads", Resource: "uploads",
		Properties: map[string]string{provider.PropertyBucket: "prod-web-r0a1b2c3d-uploads"},
	}
}

func storeManifest(t *testing.T, machine *box, options vps.Options) boxapi.Manifest {
	t.Helper()
	app := anApp()
	app.Values = provider.AppValues{Bindings: []provider.Binding{bindingBucket()}}
	p := vps.ProviderOver(options, func(context.Context) (ssh.Conn, error) { return machine, nil })
	if _, err := p.ProvisionContainers(context.Background(), aStack(t, app), progress.Discard()); err != nil {
		t.Fatalf("ProvisionContainers() = %v", err)
	}
	return manifestIn(t, machine)
}

func manifestIn(t *testing.T, machine *box) boxapi.Manifest {
	t.Helper()
	return manifestFed(t, machine.feeds())
}

func manifestFed(t *testing.T, fed []string) boxapi.Manifest {
	t.Helper()
	for _, sent := range fed {
		for line := range strings.SplitSeq(sent, "\n") {
			raw, found := strings.CutPrefix(line, boxapi.EnvVar+"=")
			if !found {
				continue
			}
			parsed, err := boxapi.Parse([]byte(raw))
			if err != nil {
				t.Fatal(err)
			}
			return parsed
		}
	}
	t.Fatal("nothing the deploy sent to the box contained a live manifest for the app")
	return boxapi.Manifest{}
}

func TestAnAppBindingABucketIsHandedItsStoreSealedAndNeverInPlaintext(t *testing.T) {
	t.Parallel()

	machine := &box{kept: sealedRootKey()}
	manifest := storeManifest(t, machine, vps.Options{SSH: vps.Target{Host: "box.invalid", User: "ada"}})
	if manifest.Store == nil {
		t.Fatal("the manifest names no store, so the runtime in front of the app has nothing to sign against")
	}
	if manifest.Store.Pointer != router.DefaultPointer {
		t.Errorf("the manifest names pointer %q, and the box answers the store's public address out of what that pointer claims", manifest.Store.Pointer)
	}
	if manifest.Store.Endpoint != "http://shop-prod-infra-store-s3:9000" || !manifest.Store.PathStyle {
		t.Errorf("the manifest points the runtime at %+v, want the store this project runs, addressed path-style", manifest.Store)
	}
	if manifest.Store.Sealed == "" {
		t.Error("the manifest includes no sealed credential, so the runtime could reach no store")
	}
	if strings.Contains(manifest.Store.Sealed, recordedRootKey) {
		t.Errorf("the manifest includes the store credential in plaintext: %q", manifest.Store.Sealed)
	}
	if manifest.Store.Volume == "" {
		t.Error("the manifest names no volume, so the disk guard has nothing to measure")
	}
	if !strings.HasPrefix(manifest.Store.Sessions, s3store.SessionsBucket()+"/") {
		t.Errorf("upload sessions live in %q, want a prefix of the store's own bucket: a session kept inside a declared bucket is stranded the day that bucket is dropped",
			manifest.Store.Sessions)
	}
}

func TestTheStoreKeepsItsSessionsInABucketNoAppDeclaresOrReaches(t *testing.T) {
	t.Parallel()

	machine := &box{}
	if _, err := over(machine).ProvisionBucket(context.Background(), aBucket(t, "uploads", true), progress.Discard()); err != nil {
		t.Fatalf("Bucket() = %v", err)
	}
	joined := strings.Join(machine.commands(), "\n")
	if !strings.Contains(joined, "/"+s3store.SessionsBucket()) {
		t.Fatalf("provisioning a store created no bucket for its sessions:\n%s", joined)
	}
	for line := range strings.SplitSeq(joined, "\n") {
		if strings.Contains(line, s3store.SessionsBucket()) && strings.Contains(line, "?policy") {
			t.Errorf("the store's own sessions bucket was given a policy of its own:\n%s", line)
		}
	}
}

func TestDroppingADeclaredBucketLeavesTheStoresSessionsWhereTheyAre(t *testing.T) {
	t.Parallel()

	machine := &box{kept: sealedRootKey()}
	err := over(machine).RemoveBucket(context.Background(),
		provider.StackRef{Project: "shop", Tier: environment.TierProduction, Name: aStackName(t)},
		bindingBucket(), progress.Discard())
	if err != nil {
		t.Fatalf("RemoveBucket() = %v", err)
	}
	if joined := strings.Join(machine.commands(), "\n"); strings.Contains(joined, s3store.SessionsBucket()) {
		t.Errorf("dropping one declared bucket reached for the store's sessions:\n%s", joined)
	}
}

func anInfraStack(t *testing.T) provider.StackRef {
	t.Helper()
	return provider.StackRef{
		Project: "shop", Tier: environment.TierProduction, Name: naming.InfraStack("prod"),
	}
}

func TestTheAssociatedDataTheRuntimeOpensTheStoreSecretUnderIsTheOneTheDeploySealedItUnder(t *testing.T) {
	t.Parallel()

	machine := &box{kept: sealedRootKey()}
	p := vps.ProviderOver(
		vps.Options{SSH: vps.Target{Host: "box.invalid", User: "ada"}},
		func(context.Context) (ssh.Conn, error) { return machine, nil },
	)
	provisioned := aBucket(t, "uploads", false)
	provisioned.Ref = anInfraStack(t)
	if _, err := p.ProvisionBucket(context.Background(), provisioned, progress.Discard()); err != nil {
		t.Fatalf("Bucket() = %v", err)
	}

	app := anApp()
	app.Values = provider.AppValues{Bindings: []provider.Binding{bindingBucket()}}
	if _, err := p.ProvisionContainers(context.Background(), aStack(t, app), progress.Discard()); err != nil {
		t.Fatalf("ProvisionContainers() = %v", err)
	}
	manifest := manifestIn(t, machine)
	if manifest.Store == nil {
		t.Fatal("the app binding a bucket was handed no store")
	}
	opened, err := boxapi.NewStoreSecretAssociatedData(manifest.Slug, environment.Tier(manifest.Tier), manifest.Store.Env)
	if err != nil {
		t.Fatal(err)
	}
	want, err := vps.NewStoreSecretAssociatedData(provisioned.Ref)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(opened.Bytes(), want.Bytes()) {
		t.Errorf("the runtime would open the store's credential at %s and the deploy sealed it at %s:"+
			" a resource is provisioned on its environment's infra stack and an app runs on its own, so associated data"+
			" naming the asking stack opens nothing", opened.Bytes(), want.Bytes())
	}
	if manifest.Store.Endpoint != "http://"+vps.StoreName(provisioned.Ref)+":9000" {
		t.Errorf("the runtime is pointed at %q, want the store this environment runs", manifest.Store.Endpoint)
	}
}

func TestAnAppBindingNoBucketIsHandedNoStore(t *testing.T) {
	t.Parallel()

	machine := &box{}
	app := anApp()
	app.Values = provider.AppValues{Secrets: []provider.SecretRef{{Key: "DATABASE_URL"}}}
	if _, err := over(machine).ProvisionContainers(context.Background(), aStack(t, app), progress.Discard()); err != nil {
		t.Fatalf("ProvisionContainers() = %v", err)
	}
	for _, command := range machine.commands() {
		if strings.Contains(command, `"store"`) {
			t.Errorf("an app binding no bucket was handed a store:\n%s", command)
		}
	}
}

func TestAnAppWhoseBucketIsBoundToAStoreIsHandedNoStoreOfTheBoxs(t *testing.T) {
	t.Parallel()

	machine := &box{}
	app := anApp()
	app.Values = provider.AppValues{Bindings: []provider.Binding{{
		Type: provider.BindingBucket, Name: "ocel:bucket.uploads", Source: "ocel.json",
		Properties: map[string]string{
			provider.PropertyBucket:   "acme",
			provider.PropertyEndpoint: "https://abc.r2.cloudflarestorage.com",
		},
	}}}
	if _, err := over(machine).ProvisionContainers(context.Background(), aStack(t, app), progress.Discard()); err != nil {
		t.Fatalf("ProvisionContainers() = %v", err)
	}
	for _, command := range machine.commands() {
		if strings.Contains(command, `"store"`) {
			t.Errorf("an app whose one bucket lives in a store of its own was handed the box's store:\n%s", command)
		}
	}
}

func TestRemovingABucketTakesItsObjectsWithIt(t *testing.T) {
	t.Parallel()

	machine := &box{kept: sealedRootKey()}
	err := over(machine).RemoveBucket(context.Background(),
		provider.StackRef{Project: "shop", Tier: environment.TierProduction, Name: aStackName(t)},
		bindingBucket(), progress.Discard())
	if err != nil {
		t.Fatalf("RemoveBucket() = %v", err)
	}
	joined := strings.Join(machine.commands(), "\n")
	if strings.Contains(joined, "rm -rf /data/") {
		t.Errorf("a bucket was taken off the store's disk behind its back, leaving the store's own record of it in place:\n%s", joined)
	}
	for _, want := range []string{"list-type=2", "?uploads", "'DELETE'"} {
		if !strings.Contains(joined, want) {
			t.Errorf("removing a bucket never asked the store for %s:\n%s", want, joined)
		}
	}
}

func TestAProvisionedStoreIsRoutedOnTheBoxsProxyUnderALabelOfItsOwn(t *testing.T) {
	t.Parallel()

	machine := &box{}
	if _, err := over(machine).ProvisionBucket(context.Background(), aBucket(t, "uploads", false), progress.Discard()); err != nil {
		t.Fatalf("Bucket() = %v", err)
	}
	state, err := host.ReadRoutingTable([]byte(machine.routingDoc))
	if err != nil {
		t.Fatalf("ReadRoutingTable() = %v", err)
	}
	at := slices.IndexFunc(state.Routes, func(route host.AppRoute) bool { return route.App == boxapi.StoreLabel })
	if at < 0 {
		t.Fatalf("provisioning a store left the proxy routing %v, and nothing off the box reaches it", state.Routes)
	}
	route := state.Routes[at]
	if route.Owner != boxapi.Surface("shop", "production") || route.Pointer != router.DefaultPointer {
		t.Errorf("the store is routed under %s/%s, want the surface and pointer its project's domains are claimed under", route.Owner, route.Pointer)
	}
	if !strings.HasSuffix(route.Upstream, ":9000") || !strings.Contains(route.Upstream, "store-s3") {
		t.Errorf("the store's route forwards to %q, want the store container's own name and port", route.Upstream)
	}
}

func aStackName(t *testing.T) naming.StackName {
	t.Helper()
	stack, err := naming.ParseStackName("prod--web--r0a1b2c3d")
	if err != nil {
		t.Fatal(err)
	}
	return stack
}

type heldStore struct {
	buckets map[string]map[string][]byte
}

func (s *heldStore) answer(command, input string) (ssh.Result, bool) {
	if s == nil || !strings.Contains(command, "'--request'") || !strings.Contains(command, "'http://127.0.0.1:9000/") {
		return ssh.Result{}, false
	}
	if !strings.Contains(command, "said=$(") {
		body, _ := base64.StdEncoding.DecodeString(strings.TrimSpace(input))
		s.call(command, body)
		return ssh.Result{}, true
	}
	var said strings.Builder
	var code string
	var answered []byte
	for _, line := range strings.Split(command, "\n") {
		switch {
		case strings.HasPrefix(line, "said=$("):
			var body []byte
			if fed, piped := strings.CutPrefix(line, "said=$(printf '%s' '"); piped {
				encoded, _, _ := strings.Cut(fed, "'")
				body, _ = base64.StdEncoding.DecodeString(encoded)
			}
			code, answered = s.call(line, body)
		case strings.HasPrefix(line, "printf '%s=%s\\n' "):
			said.WriteString(unquoted(strings.Fields(line)[2]) + "=" + code + "\n")
		case strings.HasPrefix(line, "printf '%s.body=%s\\n' "):
			said.WriteString(unquoted(strings.Fields(line)[2]) + ".body=" + base64.StdEncoding.EncodeToString(answered) + "\n")
		}
	}
	return ssh.Result{Stdout: said.String()}, true
}

func (s *heldStore) call(line string, body []byte) (string, []byte) {
	fields := strings.Fields(line)
	var method string
	var target *url.URL
	for at, field := range fields {
		if field == "'--request'" && at+1 < len(fields) {
			method = unquoted(fields[at+1])
		}
		if address, isURL := strings.CutPrefix(unquoted(strings.TrimSuffix(field, ")")), "http://127.0.0.1:9000/"); isURL {
			target, _ = url.Parse("http://127.0.0.1:9000/" + address)
		}
	}
	if target == nil {
		return "400", nil
	}
	bucket, _, _ := strings.Cut(strings.TrimPrefix(target.Path, "/"), "/")
	if s.buckets == nil {
		s.buckets = map[string]map[string][]byte{}
	}
	held, exists := s.buckets[bucket]
	query := target.RawQuery
	switch {
	case query == "" && method == http.MethodPut && exists:
		return "409", nil
	case query == "" && method == http.MethodPut:
		s.buckets[bucket] = map[string][]byte{}
		return "200", nil
	case query == "" && method == http.MethodDelete:
		delete(s.buckets, bucket)
		return "204", nil
	case !exists:
		return "404", nil
	case query == "location":
		return "200", []byte("<LocationConstraint/>")
	case strings.HasPrefix(query, "uploads"):
		return "200", []byte("<ListMultipartUploadsResult></ListMultipartUploadsResult>")
	case strings.HasPrefix(query, "list-type"):
		return "200", []byte("<ListBucketResult></ListBucketResult>")
	case method == http.MethodGet:
		if config, set := held[query]; set {
			return "200", config
		}
		return "404", nil
	case method == http.MethodPut:
		held[query] = body
		return "200", nil
	case method == http.MethodDelete:
		delete(held, query)
		return "204", nil
	default:
		return "200", nil
	}
}

func bucketProvisioned(t *testing.T, machine *box, in resources.ProvisionRequest) provider.Binding {
	t.Helper()
	binding, err := over(machine).ProvisionBucket(context.Background(), in, progress.Discard())
	if err != nil {
		t.Fatalf("ProvisionBucket() = %v", err)
	}
	return binding
}

func plannedBucket(t *testing.T, machine *box, in resources.ProvisionRequest, recorded *provider.Binding) (provider.ChangeAction, string) {
	t.Helper()
	mark := len(machine.commands())
	action, reason, err := over(machine).PlanBucket(context.Background(), in, recorded)
	if err != nil {
		t.Fatalf("PlanBucket() = %v", err)
	}
	refuseWrites(t, machine.since(mark))
	return action, reason
}

func TestABucketTurningPublicPlansAnUpdate(t *testing.T) {
	t.Parallel()

	machine := &box{remembers: true, store: &heldStore{}}
	recorded := bucketProvisioned(t, machine, aBucket(t, "uploads", false))
	action, reason := plannedBucket(t, machine, aBucket(t, "uploads", true), &recorded)
	if action != provider.ActionUpdate || !strings.Contains(reason, "becomes readable by anyone") {
		t.Errorf("a private bucket declared public plans %s %q, and the apply opens it to anonymous reads", action, reason)
	}
}

func TestABucketTurningPrivatePlansAnUpdate(t *testing.T) {
	t.Parallel()

	machine := &box{remembers: true, store: &heldStore{}}
	recorded := bucketProvisioned(t, machine, aBucket(t, "uploads", true))
	action, reason := plannedBucket(t, machine, aBucket(t, "uploads", false), &recorded)
	if action != provider.ActionUpdate || !strings.Contains(reason, "stops being readable by anyone") {
		t.Errorf("a public bucket declared private plans %s %q, and the apply closes it to anonymous reads", action, reason)
	}
}

func TestABucketWhoseOriginsChangePlansAnUpdate(t *testing.T) {
	t.Parallel()

	machine := &box{remembers: true, store: &heldStore{}}
	recorded := bucketProvisioned(t, machine, aBucket(t, "uploads", false))
	widened := aBucket(t, "uploads", false)
	widened.Resource.Bucket.AllowedOrigins = append(widened.Resource.Bucket.AllowedOrigins, "https://admin.example.com")
	action, reason := plannedBucket(t, machine, widened, &recorded)
	if action != provider.ActionUpdate || !strings.Contains(reason, "https://admin.example.com") {
		t.Errorf("a bucket gaining an origin plans %s %q, and the apply rewrites its CORS", action, reason)
	}
}

func TestAnUnchangedBucketPlansKeep(t *testing.T) {
	t.Parallel()

	for _, public := range []bool{false, true} {
		machine := &box{remembers: true, store: &heldStore{}}
		recorded := bucketProvisioned(t, machine, aBucket(t, "uploads", public))
		if action, reason := plannedBucket(t, machine, aBucket(t, "uploads", public), &recorded); action != provider.ActionKeep {
			t.Errorf("a bucket (public %t) declared as it was provisioned plans %s %q, want keep", public, action, reason)
		}
	}
}

func TestABucketTheStoreDoesNotHoldPlansACreate(t *testing.T) {
	t.Parallel()

	machine := &box{remembers: true, store: &heldStore{}}
	bucketProvisioned(t, machine, aBucket(t, "uploads", false))
	if action, reason := plannedBucket(t, machine, aBucket(t, "avatars", false), nil); action != provider.ActionCreate {
		t.Errorf("a bucket the store does not hold plans %s %q, want create", action, reason)
	}
}

func TestABucketOnABoxRunningNoStorePlansACreateAndKeepsNothing(t *testing.T) {
	t.Parallel()

	machine := &box{remembers: true, store: &heldStore{}}
	if action, reason := plannedBucket(t, machine, aBucket(t, "uploads", false), nil); action != provider.ActionCreate {
		t.Errorf("a bucket on a box running no store plans %s %q, want create", action, reason)
	}
}
