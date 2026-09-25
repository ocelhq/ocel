package gcp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"google.golang.org/api/cloudscheduler/v1"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

type pushedImages struct {
	mu   sync.Mutex
	refs []string
}

func (p *pushedImages) based(context.Context, string) (v1.Image, error) { return empty.Image, nil }

func (p *pushedImages) pushImage(_ context.Context, _ providerkit.Class, _, ref string, _ v1.Image, _ providerkit.Reporter) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.refs = append(p.refs, ref)
	return nil
}

type syncServer struct {
	*iamServer

	services      map[string]json.RawMessage
	servicePolicy map[string]json.RawMessage
	schedules     map[string]json.RawMessage
	patched       []string
	deleted       []string
}

func standingSync() *syncServer {
	return &syncServer{
		iamServer:     standingIAM(),
		services:      map[string]json.RawMessage{},
		servicePolicy: map[string]json.RawMessage{},
		schedules:     map[string]json.RawMessage{},
	}
}

const doneOperation = `{"name":"projects/acme-prod/locations/europe-west1/operations/op","done":true}`

func servedAt(name string) string { return "https://" + name + "-3k2x7lq4ta-ew.a.run.app" }

func withURI(body []byte, name string) json.RawMessage {
	var held map[string]any
	_ = json.Unmarshal(body, &held)
	held["uri"] = servedAt(name)
	out, _ := json.Marshal(held)
	return out
}

func (s *syncServer) open(t *testing.T) bootstrapper {
	t.Helper()
	iam := s.rest(t)
	c := s.serve(t, func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		serving := strings.HasPrefix(path, "/v2/") && strings.Contains(path, "/locations/europe-west1/services")
		scheduling := strings.HasPrefix(path, "/v1/") && strings.Contains(path, "/locations/europe-west1/jobs")
		if !serving && !scheduling {
			iam(w, r)
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		body, _ := io.ReadAll(r.Body)
		name, _, _ := strings.Cut(strings.TrimPrefix(strings.TrimPrefix(path, "/v2/"), "/v1/"), ":")
		held := s.schedules
		if serving {
			held = s.services
		}
		switch {
		case strings.HasSuffix(path, ":getIamPolicy"):
			if policy := s.servicePolicy[name]; policy != nil {
				_, _ = w.Write(policy)
				return
			}
			_, _ = w.Write([]byte(`{"etag":"BwXhoLA="}`))
		case strings.HasSuffix(path, ":setIamPolicy"):
			var asked struct {
				Policy json.RawMessage `json:"policy"`
			}
			_ = json.Unmarshal(body, &asked)
			s.servicePolicy[name] = asked.Policy
			_, _ = w.Write(asked.Policy)
		case r.Method == http.MethodPost && serving:
			service := r.URL.Query().Get("serviceId")
			held[name+"/"+service] = withURI(body, service)
			_, _ = w.Write([]byte(doneOperation))
		case r.Method == http.MethodPost:
			var named struct {
				Name string `json:"name"`
			}
			_ = json.Unmarshal(body, &named)
			key := named.Name
			if held[key] != nil {
				w.WriteHeader(http.StatusConflict)
				_, _ = w.Write([]byte(`{"error":{"code":409,"message":"already exists"}}`))
				return
			}
			held[key] = body
			_, _ = w.Write(body)
		case r.Method == http.MethodPatch:
			s.patched = append(s.patched, name)
			if serving {
				held[name] = withURI(body, name[strings.LastIndex(name, "/")+1:])
				_, _ = w.Write([]byte(doneOperation))
				return
			}
			held[name] = body
			_, _ = w.Write(body)
		case r.Method == http.MethodDelete:
			s.deleted = append(s.deleted, name)
			if held[name] == nil {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"error":{"code":404,"message":"not found"}}`))
				return
			}
			delete(held, name)
			if serving {
				_, _ = w.Write([]byte(doneOperation))
				return
			}
			_, _ = w.Write([]byte(`{}`))
		case r.Method == http.MethodGet && held[name] != nil:
			_, _ = w.Write(held[name])
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":404,"message":"not found"}}`))
		}
	})
	return bootstrapper{clients: c}
}

func (s *syncServer) held(held map[string]json.RawMessage, name string, into any) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	body := held[name]
	if body == nil {
		return false
	}
	return json.Unmarshal(body, into) == nil
}

func (s *syncServer) edit(held map[string]json.RawMessage, name, from, to string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	held[name] = json.RawMessage(strings.Replace(string(held[name]), from, to, 1))
}

func idsOf(items []item) []string {
	ids := make([]string, 0, len(items))
	for _, held := range items {
		ids = append(ids, held.ID())
	}
	return ids
}

func TestEachClassStandsAnEnvSyncerBesideItsKey(t *testing.T) {
	t.Parallel()
	names := Names{namespace: "ocel", project: "acme-prod"}

	for _, class := range []providerkit.Class{providerkit.ClassProduction, providerkit.ClassPreview} {
		stood := idsOf(bootstrapItems(names, class, false))
		for _, want := range []item{
			{Kind: KindServiceAccount, Name: names.EnvSyncAccount(class)},
			{Kind: KindServiceAccount, Name: names.EnvSyncInvoker(class)},
			{Kind: KindService, Name: names.EnvSync(class)},
			{Kind: KindSchedule, Name: names.EnvSync(class)},
		} {
			if !slices.Contains(stood, want.ID()) {
				t.Errorf("a %s bootstrap stands %v, want %s among them", class, stood, want.ID())
			}
		}
		key := slices.Index(stood, item{Kind: KindKey, Name: string(class)}.ID())
		repository := slices.Index(stood, item{Kind: KindRepository, Name: names.Repository(class)}.ID())
		service := slices.Index(stood, item{Kind: KindService, Name: names.EnvSync(class)}.ID())
		schedule := slices.Index(stood, item{Kind: KindSchedule, Name: names.EnvSync(class)}.ID())
		if service < key || service < repository || schedule < service {
			t.Errorf("a %s bootstrap stands %v, want the key and the repository before the service, and the service before the schedule that calls it", class, stood)
		}
	}
}

func TestTheEmulatorStandsTheSyncerServiceAndScheduleButNoRepository(t *testing.T) {
	t.Parallel()
	names := Names{namespace: "ocel", project: "acme-prod"}

	stood := idsOf(bootstrapItems(names, providerkit.ClassProduction, true))
	if slices.Contains(stood, item{Kind: KindRepository, Name: names.Repository(providerkit.ClassProduction)}.ID()) {
		t.Errorf("an emulated bootstrap stands %v, and no emulator serves Artifact Registry", stood)
	}
	for _, want := range []item{
		{Kind: KindServiceAccount, Name: names.EnvSyncAccount(providerkit.ClassProduction)},
		{Kind: KindService, Name: names.EnvSync(providerkit.ClassProduction)},
		{Kind: KindSchedule, Name: names.EnvSync(providerkit.ClassProduction)},
	} {
		if !slices.Contains(stood, want.ID()) {
			t.Errorf("an emulated bootstrap stands %v, want %s among them: the emulator serves Cloud Run services", stood, want.ID())
		}
	}
}

func TestTheEnvSyncerAccountsFitTheLongestNamespaceARuntimeAccountDoes(t *testing.T) {
	t.Parallel()
	names := Names{namespace: providerkit.Namespace(strings.Repeat("a", maxAccountID-len("-"+string(longestClass)))), project: "acme-prod"}
	if err := names.fit(); err != nil {
		t.Fatalf("fit() = %v", err)
	}
	for _, class := range []providerkit.Class{providerkit.ClassProduction, providerkit.ClassPreview} {
		for _, account := range []string{names.EnvSyncAccount(class), names.EnvSyncInvoker(class)} {
			if len(account) > maxAccountID {
				t.Errorf("%s is %d characters and IAM takes %d, so the longest namespace the runtime account leaves room for would stand no syncer", account, len(account), maxAccountID)
			}
		}
	}
	if names.EnvSyncAccount(providerkit.ClassProduction) == names.EnvSyncAccount(providerkit.ClassPreview) ||
		names.EnvSyncInvoker(providerkit.ClassProduction) == names.EnvSyncInvoker(providerkit.ClassPreview) {
		t.Error("both classes' syncers share an account, and removing one class would take the other's")
	}
}

func TestTheSyncerAccountIsHeldToThisDatabaseAndSealingAndOpeningUnderItsClassKeyAlone(t *testing.T) {
	t.Parallel()
	server := standingSync()
	b := server.open(t)
	read := survey{Class: providerkit.ClassProduction, Names: b.clients.Names}
	ctx := context.Background()
	name := b.clients.EnvSyncAccount(providerkit.ClassProduction)

	if err := b.makeAccount(ctx, read, name); err != nil {
		t.Fatalf("makeAccount() = %v", err)
	}
	member := "serviceAccount:" + b.clients.EnvSyncAccountEmail(providerkit.ClassProduction)
	members, condition := server.projectMembers(envSyncRecordsRole)
	if !slices.Contains(members, member) {
		t.Errorf("the project binds %v to %s, want the syncer: it writes what a source holds into the records", members, envSyncRecordsRole)
	}
	if condition == nil || !strings.Contains(condition.Expression, "projects/acme-prod/databases/ocel") {
		t.Errorf("the syncer's records role is conditioned on %+v, want this namespace's one database", condition)
	}
	key := "projects/acme-prod/locations/europe-west1/keyRings/ocel/cryptoKeys/production"
	for _, role := range []string{envSyncSealingRole, envSyncOpeningRole} {
		if held := server.keyMembers(key, role); !slices.Contains(held, member) {
			t.Errorf("the production key binds %v to %s, want the syncer: it opens its credentials and seals what it mirrors", held, role)
		}
	}
	if held, _ := server.projectMembers(runtimeRecordsRole); slices.Contains(held, member) {
		t.Errorf("the syncer holds the runtime's %s too, and one grant is what it needs", runtimeRecordsRole)
	}

	stands, err := b.accountStands(ctx, providerkit.ClassProduction, name)
	if err != nil {
		t.Fatalf("accountStands() = %v", err)
	}
	if !stands.held || stands.mends != "" {
		t.Errorf("accountStands() = %+v after the grants landed, want it standing with nothing to mend", stands)
	}

	if err := b.takeAccount(ctx, providerkit.ClassProduction, name); err != nil {
		t.Fatalf("takeAccount() = %v", err)
	}
	if held, _ := server.projectMembers(envSyncRecordsRole); slices.Contains(held, member) {
		t.Errorf("the project still binds the deleted syncer to %s", envSyncRecordsRole)
	}
	for _, role := range []string{envSyncSealingRole, envSyncOpeningRole} {
		if held := server.keyMembers(key, role); slices.Contains(held, member) {
			t.Errorf("the key still binds the deleted syncer to %s", role)
		}
	}
}

func TestASyncerAccountThatMayNotWriteIsSurveyedAsMendable(t *testing.T) {
	t.Parallel()
	b := standingSync().open(t)

	stands, err := b.accountStands(context.Background(), providerkit.ClassProduction, b.clients.EnvSyncAccount(providerkit.ClassProduction))
	if err != nil {
		t.Fatalf("accountStands() = %v", err)
	}
	if !stands.held || stands.mends != reasonUnsynced {
		t.Errorf("accountStands() = %+v, want it standing and mended for the writes it lacks", stands)
	}
}

func TestTheInvokerAccountIsGrantedNothingOnTheProjectOrTheKey(t *testing.T) {
	t.Parallel()
	server := standingSync()
	b := server.open(t)
	read := survey{Class: providerkit.ClassPreview, Names: b.clients.Names}
	ctx := context.Background()

	if err := b.makeAccount(ctx, read, b.clients.EnvSyncInvoker(providerkit.ClassPreview)); err != nil {
		t.Fatalf("makeAccount() = %v", err)
	}
	if server.project != nil && len(server.project.Bindings) > 0 {
		t.Errorf("the project binds %+v, and the invoker calls one service and reads nothing", server.project.Bindings)
	}
	stands, err := b.accountStands(ctx, providerkit.ClassPreview, b.clients.EnvSyncInvoker(providerkit.ClassPreview))
	if err != nil || !stands.held || stands.mends != "" {
		t.Errorf("accountStands() = %+v, %v, want it standing with nothing to mend", stands, err)
	}
}

type heldService struct {
	Ingress            string `json:"ingress"`
	InvokerIamDisabled *bool  `json:"invokerIamDisabled"`
	URI                string `json:"uri"`
	Template           struct {
		Containers []struct {
			Image string `json:"image"`
			Env   []struct {
				Name  string `json:"name"`
				Value string `json:"value"`
			} `json:"env"`
			Resources struct {
				CPUIdle *bool             `json:"cpuIdle"`
				Limits  map[string]string `json:"limits"`
			} `json:"resources"`
		} `json:"containers"`
		Scaling struct {
			MinInstanceCount *int `json:"minInstanceCount"`
			MaxInstanceCount int  `json:"maxInstanceCount"`
		} `json:"scaling"`
		MaxInstanceRequestConcurrency int    `json:"maxInstanceRequestConcurrency"`
		ExecutionEnvironment          string `json:"executionEnvironment"`
		ServiceAccount                string `json:"serviceAccount"`
		Timeout                       string `json:"timeout"`
	} `json:"template"`
}

type heldPolicy struct {
	Bindings []struct {
		Role    string   `json:"role"`
		Members []string `json:"members"`
	} `json:"bindings"`
}

func TestTheSyncerServiceRunsTheCarriedSyncerAsItsOwnAccountAndOnlyTheInvokerMayCallIt(t *testing.T) {
	t.Parallel()
	server := standingSync()
	images := &pushedImages{}
	b := server.open(t)
	b.images = images
	ctx := context.Background()
	class := providerkit.ClassProduction
	name := b.clients.EnvSync(class)
	path := b.clients.servicePath(name)

	if err := b.makeService(ctx, class, name); err != nil {
		t.Fatalf("makeService() = %v", err)
	}
	want := envSyncRef(b.clients, class)
	if !slices.Equal(images.refs, []string{want}) {
		t.Errorf("pushed %v, want the syncer image at %s", images.refs, want)
	}
	if !strings.HasPrefix(want, "europe-west1-docker.pkg.dev/acme-prod/"+b.clients.Repository(class)+"/ocel-envsync:") {
		t.Errorf("the syncer image is %s, want it in the class's own repository", want)
	}
	var service heldService
	if !server.held(server.services, path, &service) {
		t.Fatalf("no service stands at %s", path)
	}
	task := service.Template
	if len(task.Containers) != 1 || task.Containers[0].Image != want {
		t.Fatalf("the service runs %+v, want the one image %s", task.Containers, want)
	}
	if task.ServiceAccount != b.clients.EnvSyncAccountEmail(class) {
		t.Errorf("the service runs as %q, want the syncer's own account", task.ServiceAccount)
	}
	env := map[string]string{}
	for _, held := range task.Containers[0].Env {
		env[held.Name] = held.Value
	}
	if env["OCEL_INFRA_CLASS"] != "production" || env["OCEL_NAMESPACE"] != "ocel" || env["OCEL_GCP_PROJECT"] != "acme-prod" || env["OCEL_GCP_REGION"] != "europe-west1" {
		t.Errorf("the service carries %v, want the class, namespace, project and region the syncer reads", env)
	}

	resources := task.Containers[0].Resources
	if resources.CPUIdle == nil || !*resources.CPUIdle {
		t.Errorf("the service was sent cpuIdle %v, want true: request-based billing charges only while a poll runs", resources.CPUIdle)
	}
	if resources.Limits["cpu"] != "0.08" || resources.Limits["memory"] != "128Mi" {
		t.Errorf("the service is limited to %v, want the smallest Cloud Run takes: 0.08 vCPU and 128Mi", resources.Limits)
	}
	if task.ExecutionEnvironment != "EXECUTION_ENVIRONMENT_GEN1" {
		t.Errorf("the service runs in %q, want the first generation: it alone takes under 1 vCPU and under 512Mi", task.ExecutionEnvironment)
	}
	if task.MaxInstanceRequestConcurrency != 1 {
		t.Errorf("the service takes %d requests at once, want 1: under 1 vCPU Cloud Run takes no more", task.MaxInstanceRequestConcurrency)
	}
	if task.Scaling.MinInstanceCount == nil || *task.Scaling.MinInstanceCount != 0 || task.Scaling.MaxInstanceCount != 1 {
		t.Errorf("the service scales %+v, want an explicit floor of 0 and a ceiling of 1: nothing stays warm, and two polls never race", task.Scaling)
	}
	if task.Timeout != "60s" {
		t.Errorf("a request may run %q, want 60s: the next minute's poll is the retry", task.Timeout)
	}
	if service.Ingress != "INGRESS_TRAFFIC_INTERNAL_ONLY" {
		t.Errorf("the service takes %q, want internal traffic alone: Cloud Scheduler in the same project counts as internal", service.Ingress)
	}
	if service.InvokerIamDisabled == nil || *service.InvokerIamDisabled {
		t.Errorf("the service was sent invokerIamDisabled %v, want an explicit false: a caller needs run.invoker", service.InvokerIamDisabled)
	}

	var policy heldPolicy
	if !server.held(server.servicePolicy, path, &policy) {
		t.Fatal("the service holds no policy, so Cloud Scheduler may not call it")
	}
	invoker := "serviceAccount:" + b.clients.EnvSyncInvokerEmail(class)
	if len(policy.Bindings) != 1 || policy.Bindings[0].Role != envSyncCallRole || !slices.Equal(policy.Bindings[0].Members, []string{invoker}) {
		t.Errorf("the service's policy binds %+v, want %s alone holding %s", policy.Bindings, invoker, envSyncCallRole)
	}

	stands, err := b.serviceStands(ctx, class, name)
	if err != nil || !stands.held || stands.mends != "" {
		t.Errorf("serviceStands() = %+v, %v, want it standing with nothing to mend", stands, err)
	}
}

func TestASyncerServiceRunOrReachedOtherwiseIsMendedInPlace(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ drift, from, to string }{
		{"a new provider carries a new syncer", envSyncTag(), "0000"},
		{"instance-based billing charges every idle minute", `"cpuIdle":true`, `"cpuIdle":false`},
		{"a warm instance bills around the clock", `"minInstanceCount":0`, `"minInstanceCount":1`},
		{"a second instance races the first", `"maxInstanceCount":1`, `"maxInstanceCount":3`},
		{"a bigger instance bills more", `"cpu":"0.08"`, `"cpu":"1"`},
		{"anyone on the internet may reach it", `"INGRESS_TRAFFIC_INTERNAL_ONLY"`, `"INGRESS_TRAFFIC_ALL"`},
		{"anyone may call it", `"invokerIamDisabled":false`, `"invokerIamDisabled":true`},
	} {
		t.Run(tc.drift, func(t *testing.T) {
			t.Parallel()
			server := standingSync()
			b := server.open(t)
			b.images = &pushedImages{}
			ctx := context.Background()
			class := providerkit.ClassPreview
			name := b.clients.EnvSync(class)
			if err := b.makeService(ctx, class, name); err != nil {
				t.Fatal(err)
			}
			path := b.clients.servicePath(name)
			server.edit(server.services, path, tc.from, tc.to)

			stands, err := b.serviceStands(ctx, class, name)
			if err != nil || !stands.held || stands.mends != reasonStale {
				t.Fatalf("serviceStands() = %+v, %v, want it standing and mended", stands, err)
			}
			if err := b.mend(ctx, survey{Class: class, Names: b.clients.Names}, item{Kind: KindService, Name: name}); err != nil {
				t.Fatalf("mend() = %v", err)
			}
			if !slices.Contains(server.patched, path) {
				t.Errorf("the mend patched %v, want the service updated in place", server.patched)
			}
			if stands, err := b.serviceStands(ctx, class, name); err != nil || stands.mends != "" {
				t.Errorf("serviceStands() after the mend = %+v, %v", stands, err)
			}
		})
	}
}

func TestASyncerServiceAnyoneMayCallIsMended(t *testing.T) {
	t.Parallel()
	server := standingSync()
	b := server.open(t)
	b.images = &pushedImages{}
	ctx := context.Background()
	class := providerkit.ClassProduction
	name := b.clients.EnvSync(class)
	if err := b.makeService(ctx, class, name); err != nil {
		t.Fatal(err)
	}
	path := b.clients.servicePath(name)
	server.edit(server.servicePolicy, path, `"members":[`, `"members":["allUsers",`)

	stands, err := b.serviceStands(ctx, class, name)
	if err != nil || !stands.held || stands.mends != reasonUncalled {
		t.Fatalf("serviceStands() = %+v, %v, want a service allUsers may call mended", stands, err)
	}
	if err := b.makeService(ctx, class, name); err != nil {
		t.Fatalf("makeService() over one that stands = %v", err)
	}
	var policy heldPolicy
	server.held(server.servicePolicy, path, &policy)
	invoker := "serviceAccount:" + b.clients.EnvSyncInvokerEmail(class)
	if len(policy.Bindings) != 1 || !slices.Equal(policy.Bindings[0].Members, []string{invoker}) {
		t.Errorf("the mended policy binds %+v, want %s alone", policy.Bindings, invoker)
	}
}

type heldSchedule struct {
	Schedule   string `json:"schedule"`
	TimeZone   string `json:"timeZone"`
	HTTPTarget struct {
		URI        string    `json:"uri"`
		HTTPMethod string    `json:"httpMethod"`
		OauthToken *struct{} `json:"oauthToken"`
		OidcToken  struct {
			ServiceAccountEmail string `json:"serviceAccountEmail"`
			Audience            string `json:"audience"`
		} `json:"oidcToken"`
	} `json:"httpTarget"`
}

func TestTheScheduleCallsTheServiceEveryMinuteWithAnIDTokenForTheInvoker(t *testing.T) {
	t.Parallel()
	b := bootstrapper{clients: &clients{Names: Names{namespace: "ocel", project: "acme-prod"}, region: "europe-west1"}}
	class := providerkit.ClassProduction
	name := b.clients.EnvSync(class)
	sent, err := json.Marshal(b.envSyncSchedule(class, name, servedAt(name)))
	if err != nil {
		t.Fatal(err)
	}
	var schedule heldSchedule
	if err := json.Unmarshal(sent, &schedule); err != nil {
		t.Fatal(err)
	}
	if schedule.Schedule != "* * * * *" {
		t.Errorf("the schedule fires on %q, want every minute", schedule.Schedule)
	}
	if schedule.HTTPTarget.URI != servedAt(name)+"/" || schedule.HTTPTarget.HTTPMethod != http.MethodPost {
		t.Errorf("the schedule calls %s %s, want POST on the service's root", schedule.HTTPTarget.HTTPMethod, schedule.HTTPTarget.URI)
	}
	if schedule.HTTPTarget.OidcToken.ServiceAccountEmail != b.clients.EnvSyncInvokerEmail(class) || schedule.HTTPTarget.OauthToken != nil {
		t.Errorf("the schedule signs in as %+v, want an ID token for the invoker: Cloud Run takes an ID token, not an OAuth one", schedule.HTTPTarget)
	}
	if schedule.HTTPTarget.OidcToken.Audience != servedAt(name) {
		t.Errorf("the ID token is for %q, want the service's url, the audience Cloud Run checks", schedule.HTTPTarget.OidcToken.Audience)
	}
}

func TestTheScheduleIsStoodAndMendedThroughCloudScheduler(t *testing.T) {
	t.Parallel()
	server := standingSync()
	b := server.open(t)
	b.images = &pushedImages{}
	ctx := context.Background()
	class := providerkit.ClassProduction
	name := b.clients.EnvSync(class)
	path := schedulePath(b.clients, name)

	if err := b.makeService(ctx, class, name); err != nil {
		t.Fatal(err)
	}
	if err := b.makeSchedule(ctx, class, name); err != nil {
		t.Fatalf("makeSchedule() = %v", err)
	}
	var schedule heldSchedule
	if !server.held(server.schedules, path, &schedule) {
		t.Fatalf("no schedule stands at %s", path)
	}
	if schedule.HTTPTarget.URI != servedAt(name)+"/" {
		t.Errorf("the schedule calls %s, want the url the service stands at", schedule.HTTPTarget.URI)
	}
	if stands, err := b.scheduleStands(ctx, class, name); err != nil || !stands.held || stands.mends != "" {
		t.Errorf("scheduleStands() = %+v, %v, want it standing with nothing to mend", stands, err)
	}

	server.edit(server.schedules, path, "* * * * *", "*/5 * * * *")
	if stands, err := b.scheduleStands(ctx, class, name); err != nil || stands.mends != reasonResched {
		t.Fatalf("scheduleStands() = %+v, %v, want a schedule edited by hand mended", stands, err)
	}
	if err := b.makeSchedule(ctx, class, name); err != nil {
		t.Fatalf("makeSchedule() over one that stands = %v", err)
	}
	if !slices.Contains(server.patched, path) {
		t.Errorf("patched %v, want the standing schedule patched", server.patched)
	}
	if stands, err := b.scheduleStands(ctx, class, name); err != nil || stands.mends != "" {
		t.Errorf("scheduleStands() after the mend = %+v, %v", stands, err)
	}

	server.edit(server.services, b.clients.servicePath(name), servedAt(name), "https://elsewhere.a.run.app")
	if stands, err := b.scheduleStands(ctx, class, name); err != nil || stands.mends != reasonResched {
		t.Errorf("scheduleStands() = %+v, %v, want a schedule calling a url the service no longer answers on mended", stands, err)
	}

	if err := b.takeSchedule(ctx, name); err != nil {
		t.Fatalf("takeSchedule() = %v", err)
	}
	if err := b.takeSchedule(ctx, name); err != nil {
		t.Errorf("takeSchedule() of one already gone = %v, want nothing to do", err)
	}
}

func TestAScheduleWithNoServiceToCall(t *testing.T) {
	t.Parallel()
	server := standingSync()
	b := server.open(t)
	ctx := context.Background()
	class := providerkit.ClassPreview
	name := b.clients.EnvSync(class)

	if err := b.makeSchedule(ctx, class, name); err == nil || !strings.Contains(err.Error(), name) {
		t.Errorf("makeSchedule() without the service = %v, want a refusal naming %s: there is no url to call", err, name)
	}
	server.mu.Lock()
	server.schedules[schedulePath(b.clients, name)] = json.RawMessage(`{"schedule":"* * * * *","httpTarget":{"uri":"https://gone.a.run.app/"}}`)
	server.mu.Unlock()
	if stands, err := b.scheduleStands(ctx, class, name); err != nil || !stands.held || stands.mends != reasonResched {
		t.Errorf("scheduleStands() = %+v, %v, want a schedule outliving its service mended once the service stands again", stands, err)
	}
}

func TestRemovingAClassStopsTheScheduleAndTheServiceBeforeTheKeyAndTheAccounts(t *testing.T) {
	t.Parallel()
	names := Names{namespace: "ocel", project: "acme-prod"}
	class := providerkit.ClassProduction
	read := survey{Class: class, Names: names, standing: map[string]bool{}}
	for _, held := range bootstrapItems(names, class, false) {
		read.standing[held.ID()] = true
	}

	var order []string
	for _, taking := range removals(read) {
		order = append(order, taking.item.ID())
	}
	at := func(held item) int { return slices.Index(order, held.ID()) }
	schedule := at(item{Kind: KindSchedule, Name: names.EnvSync(class)})
	service := at(item{Kind: KindService, Name: names.EnvSync(class)})
	for _, later := range []item{
		{Kind: KindKey, Name: string(class)},
		{Kind: KindServiceAccount, Name: names.EnvSyncAccount(class)},
		{Kind: KindServiceAccount, Name: names.EnvSyncInvoker(class)},
		{Kind: KindServiceAccount, Name: names.RuntimeAccount(class)},
		{Kind: KindRepository, Name: names.Repository(class)},
	} {
		if schedule < 0 || service < 0 || schedule > service || service > at(later) {
			t.Errorf("removal runs %v, want the schedule, then the service, then %s", order, later.ID())
		}
	}
	if last := order[len(order)-1]; last != (item{Kind: KindBucket, Name: names.Bucket(class)}).ID() {
		t.Errorf("removal ends on %s, want the bucket holding the stamp: a removal stopped part way reads as unfinished only while it stands", last)
	}
	if len(order) != len(bootstrapItems(names, class, false)) {
		t.Errorf("removal takes %d items, want every one of the %d the bootstrap stands", len(order), len(bootstrapItems(names, class, false)))
	}
}

func TestTakingTheSyncerServiceTwiceIsNothingToDo(t *testing.T) {
	t.Parallel()
	server := standingSync()
	b := server.open(t)
	b.images = &pushedImages{}
	ctx := context.Background()
	name := b.clients.EnvSync(providerkit.ClassProduction)
	if err := b.makeService(ctx, providerkit.ClassProduction, name); err != nil {
		t.Fatal(err)
	}
	if err := b.takeService(ctx, name); err != nil {
		t.Fatalf("takeService() = %v", err)
	}
	if stands, err := b.serviceStands(ctx, providerkit.ClassProduction, name); err != nil || stands.held {
		t.Errorf("serviceStands() after takeService() = %+v, %v, want it gone", stands, err)
	}
	if err := b.takeService(ctx, name); err != nil {
		t.Errorf("takeService() of one already gone = %v, want nothing to do", err)
	}
}

func TestABootstrapChecksThePermissionsTheEnvSyncerNeeds(t *testing.T) {
	t.Parallel()
	for _, permission := range []string{
		"run.services.create", "run.services.get", "run.services.update", "run.services.delete",
		"run.services.getIamPolicy", "run.services.setIamPolicy",
		"cloudscheduler.jobs.create", "cloudscheduler.jobs.get", "cloudscheduler.jobs.update", "cloudscheduler.jobs.delete",
		"iam.serviceAccounts.actAs", "artifactregistry.repositories.uploadArtifacts",
	} {
		if !slices.Contains(bootstrapPermissions, permission) {
			t.Errorf("a bootstrap does not check %s, and the apply would fail standing the env syncer", permission)
		}
	}
	for _, permission := range bootstrapPermissions {
		if strings.HasPrefix(permission, "run.jobs.") {
			t.Errorf("a bootstrap checks %s, and it stands no Cloud Run job", permission)
		}
	}
	if !slices.Contains(rolesFor(providerkit.TierBootstrap), "roles/cloudscheduler.admin") {
		t.Errorf("the bootstrap tier grants %v, want roles/cloudscheduler.admin among them: `ocel permissions` names what a bootstrap does", rolesFor(providerkit.TierBootstrap))
	}
	for _, role := range []string{"roles/cloudscheduler.admin", "roles/run.admin", "roles/iam.serviceAccountUser"} {
		if !slices.Contains(rolesCovering(nil), role) {
			t.Errorf("a refusal over a missing permission names %v, want %s among them: it covers what standing the env syncer checks", rolesCovering(nil), role)
		}
	}
	if slices.Contains(rolesFor(providerkit.TierDeploy), "roles/cloudscheduler.admin") {
		t.Error("the deploy tier grants roles/cloudscheduler.admin, and a deploy stands no schedule")
	}
	if !slices.Contains(BootstrapAPIs, "cloudscheduler.googleapis.com") {
		t.Errorf("a bootstrap checks %v are on, want cloudscheduler.googleapis.com among them", BootstrapAPIs)
	}
}

func TestAScheduleCallingAsAnotherAccountIsMended(t *testing.T) {
	t.Parallel()
	b := bootstrapper{clients: &clients{Names: Names{namespace: "ocel", project: "acme-prod"}, region: "europe-west1"}}
	class := providerkit.ClassProduction
	uri := servedAt(b.clients.EnvSync(class))
	want := b.envSyncSchedule(class, b.clients.EnvSync(class), uri)
	held := func(email, audience string) *cloudscheduler.Job {
		copied := *want
		target := *want.HttpTarget
		target.OidcToken = nil
		if email != "" {
			target.OidcToken = &cloudscheduler.OidcToken{ServiceAccountEmail: email, Audience: audience}
		}
		copied.HttpTarget = &target
		return &copied
	}
	invoker := b.clients.EnvSyncInvokerEmail(class)
	if !sameSchedule(held(invoker, uri), want, false) {
		t.Error("the schedule this bootstrap stood reads as another")
	}
	if sameSchedule(held("someone@acme-prod.iam.gserviceaccount.com", uri), want, false) {
		t.Error("a schedule calling the service as another account reads as current, and that account may call what it likes")
	}
	if sameSchedule(held(invoker, "https://elsewhere.a.run.app"), want, false) {
		t.Error("a schedule minting a token for another audience reads as current, and Cloud Run refuses every call it makes")
	}
	if sameSchedule(held("", ""), want, false) {
		t.Error("a schedule that signs in as nobody reads as current, and Cloud Run refuses every call it makes")
	}
	if !sameSchedule(held("", ""), want, true) {
		t.Error("under the emulator, which keeps no token on a schedule, the schedule this bootstrap stood reads as another")
	}
}
