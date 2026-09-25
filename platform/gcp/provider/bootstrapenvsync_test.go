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

	jobs      map[string]json.RawMessage
	jobPolicy map[string]json.RawMessage
	schedules map[string]json.RawMessage
	patched   []string
	deleted   []string
}

func standingSync() *syncServer {
	return &syncServer{
		iamServer: standingIAM(),
		jobs:      map[string]json.RawMessage{},
		jobPolicy: map[string]json.RawMessage{},
		schedules: map[string]json.RawMessage{},
	}
}

const doneOperation = `{"name":"projects/acme-prod/locations/europe-west1/operations/op","done":true}`

func (s *syncServer) open(t *testing.T) bootstrapper {
	t.Helper()
	iam := s.rest(t)
	c := s.serve(t, func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if !strings.Contains(path, "/locations/europe-west1/jobs") {
			iam(w, r)
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		body, _ := io.ReadAll(r.Body)
		name, _, _ := strings.Cut(strings.TrimPrefix(strings.TrimPrefix(path, "/v2/"), "/v1/"), ":")
		held := s.schedules
		if strings.HasPrefix(path, "/v2/") {
			held = s.jobs
		}
		switch {
		case strings.HasSuffix(path, ":getIamPolicy"):
			if policy := s.jobPolicy[name]; policy != nil {
				_, _ = w.Write(policy)
				return
			}
			_, _ = w.Write([]byte(`{"etag":"BwXhoLA="}`))
		case strings.HasSuffix(path, ":setIamPolicy"):
			var asked struct {
				Policy json.RawMessage `json:"policy"`
			}
			_ = json.Unmarshal(body, &asked)
			s.jobPolicy[name] = asked.Policy
			_, _ = w.Write(asked.Policy)
		case r.Method == http.MethodPost && strings.HasPrefix(path, "/v2/"):
			held[name+"/"+r.URL.Query().Get("jobId")] = body
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
			held[name] = body
			s.patched = append(s.patched, name)
			if strings.HasPrefix(path, "/v2/") {
				_, _ = w.Write([]byte(doneOperation))
				return
			}
			_, _ = w.Write(body)
		case r.Method == http.MethodDelete:
			s.deleted = append(s.deleted, name)
			if held[name] == nil {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"error":{"code":404,"message":"not found"}}`))
				return
			}
			delete(held, name)
			if strings.HasPrefix(path, "/v2/") {
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
			{Kind: KindJob, Name: names.EnvSyncJob(class)},
			{Kind: KindSchedule, Name: names.EnvSyncJob(class)},
		} {
			if !slices.Contains(stood, want.ID()) {
				t.Errorf("a %s bootstrap stands %v, want %s among them", class, stood, want.ID())
			}
		}
		key := slices.Index(stood, item{Kind: KindKey, Name: string(class)}.ID())
		repository := slices.Index(stood, item{Kind: KindRepository, Name: names.Repository(class)}.ID())
		job := slices.Index(stood, item{Kind: KindJob, Name: names.EnvSyncJob(class)}.ID())
		schedule := slices.Index(stood, item{Kind: KindSchedule, Name: names.EnvSyncJob(class)}.ID())
		if job < key || job < repository || schedule < job {
			t.Errorf("a %s bootstrap stands %v, want the key and the repository before the job, and the job before its schedule", class, stood)
		}
	}
}

func TestTheEmulatorStandsTheScheduleAndAccountsButNoJob(t *testing.T) {
	t.Parallel()
	names := Names{namespace: "ocel", project: "acme-prod"}

	stood := idsOf(bootstrapItems(names, providerkit.ClassProduction, true))
	if slices.Contains(stood, item{Kind: KindJob, Name: names.EnvSyncJob(providerkit.ClassProduction)}.ID()) {
		t.Errorf("an emulated bootstrap stands %v, and no emulator serves Cloud Run jobs or the repository their image sits in", stood)
	}
	for _, want := range []item{
		{Kind: KindServiceAccount, Name: names.EnvSyncAccount(providerkit.ClassProduction)},
		{Kind: KindSchedule, Name: names.EnvSyncJob(providerkit.ClassProduction)},
	} {
		if !slices.Contains(stood, want.ID()) {
			t.Errorf("an emulated bootstrap stands %v, want %s among them", stood, want.ID())
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
		t.Errorf("the project binds %+v, and the invoker starts one job and reads nothing", server.project.Bindings)
	}
	stands, err := b.accountStands(ctx, providerkit.ClassPreview, b.clients.EnvSyncInvoker(providerkit.ClassPreview))
	if err != nil || !stands.held || stands.mends != "" {
		t.Errorf("accountStands() = %+v, %v, want it standing with nothing to mend", stands, err)
	}
}

type heldJob struct {
	Template struct {
		TaskCount int `json:"taskCount"`
		Template  struct {
			Containers []struct {
				Image string `json:"image"`
				Env   []struct {
					Name  string `json:"name"`
					Value string `json:"value"`
				} `json:"env"`
			} `json:"containers"`
			ServiceAccount string `json:"serviceAccount"`
			MaxRetries     *int   `json:"maxRetries"`
			Timeout        string `json:"timeout"`
		} `json:"template"`
	} `json:"template"`
}

func TestTheJobRunsTheCarriedSyncerAsItsOwnAccountAndOnlyTheInvokerMayStartIt(t *testing.T) {
	t.Parallel()
	server := standingSync()
	images := &pushedImages{}
	b := server.open(t)
	b.images = images
	ctx := context.Background()
	class := providerkit.ClassProduction
	name := b.clients.EnvSyncJob(class)
	path := jobPath(b.clients, name)

	if err := b.makeJob(ctx, class, name); err != nil {
		t.Fatalf("makeJob() = %v", err)
	}
	want := envSyncRef(b.clients, class)
	if !slices.Equal(images.refs, []string{want}) {
		t.Errorf("pushed %v, want the syncer image at %s", images.refs, want)
	}
	if !strings.HasPrefix(want, "europe-west1-docker.pkg.dev/acme-prod/"+b.clients.Repository(class)+"/ocel-envsync:") {
		t.Errorf("the syncer image is %s, want it in the class's own repository", want)
	}
	var job heldJob
	if !server.held(server.jobs, path, &job) {
		t.Fatalf("no job stands at %s", path)
	}
	task := job.Template.Template
	if len(task.Containers) != 1 || task.Containers[0].Image != want {
		t.Errorf("the job runs %+v, want the one image %s", task.Containers, want)
	}
	if task.ServiceAccount != b.clients.EnvSyncAccountEmail(class) {
		t.Errorf("the job runs as %q, want the syncer's own account", task.ServiceAccount)
	}
	if task.MaxRetries == nil || *task.MaxRetries != 0 {
		t.Errorf("the job was sent maxRetries %v, want an explicit 0: Cloud Run retries a task 3 times when none is sent, and the next minute is the retry", task.MaxRetries)
	}
	env := map[string]string{}
	for _, held := range task.Containers[0].Env {
		env[held.Name] = held.Value
	}
	if env["OCEL_INFRA_CLASS"] != "production" || env["OCEL_NAMESPACE"] != "ocel" || env["OCEL_GCP_PROJECT"] != "acme-prod" || env["OCEL_GCP_REGION"] != "europe-west1" {
		t.Errorf("the job carries %v, want the class, namespace, project and region the syncer reads", env)
	}

	var policy struct {
		Bindings []struct {
			Role    string   `json:"role"`
			Members []string `json:"members"`
		} `json:"bindings"`
	}
	if !server.held(server.jobPolicy, path, &policy) {
		t.Fatal("the job holds no policy, so Cloud Scheduler may not start it")
	}
	invoker := "serviceAccount:" + b.clients.EnvSyncInvokerEmail(class)
	if len(policy.Bindings) != 1 || policy.Bindings[0].Role != envSyncStartRole || !slices.Equal(policy.Bindings[0].Members, []string{invoker}) {
		t.Errorf("the job's policy binds %+v, want %s alone holding %s", policy.Bindings, invoker, envSyncStartRole)
	}

	stands, err := b.jobStands(ctx, class, name)
	if err != nil || !stands.held || stands.mends != "" {
		t.Errorf("jobStands() = %+v, %v, want it standing with nothing to mend", stands, err)
	}
}

func TestAJobRunningAnotherSyncerIsMendedInPlace(t *testing.T) {
	t.Parallel()
	server := standingSync()
	b := server.open(t)
	b.images = &pushedImages{}
	ctx := context.Background()
	class := providerkit.ClassPreview
	name := b.clients.EnvSyncJob(class)
	if err := b.makeJob(ctx, class, name); err != nil {
		t.Fatal(err)
	}
	path := jobPath(b.clients, name)
	server.mu.Lock()
	server.jobs[path] = json.RawMessage(strings.Replace(string(server.jobs[path]), envSyncTag(), "0000", 1))
	server.mu.Unlock()

	stands, err := b.jobStands(ctx, class, name)
	if err != nil || !stands.held || stands.mends != reasonStale {
		t.Fatalf("jobStands() = %+v, %v, want it standing and mended: a new provider carries a new syncer", stands, err)
	}
	if err := b.mend(ctx, survey{Class: class, Names: b.clients.Names}, item{Kind: KindJob, Name: name}); err != nil {
		t.Fatalf("mend() = %v", err)
	}
	if !slices.Contains(server.patched, path) {
		t.Errorf("the mend patched %v, want the job updated in place", server.patched)
	}
	if stands, err := b.jobStands(ctx, class, name); err != nil || stands.mends != "" {
		t.Errorf("jobStands() after the mend = %+v, %v", stands, err)
	}
}

type heldSchedule struct {
	Schedule   string `json:"schedule"`
	TimeZone   string `json:"timeZone"`
	HTTPTarget struct {
		URI        string `json:"uri"`
		HTTPMethod string `json:"httpMethod"`
		OauthToken struct {
			ServiceAccountEmail string `json:"serviceAccountEmail"`
		} `json:"oauthToken"`
		OidcToken *struct{} `json:"oidcToken"`
	} `json:"httpTarget"`
}

func TestTheScheduleStartsTheJobEveryMinuteAsTheInvoker(t *testing.T) {
	t.Parallel()
	server := standingSync()
	b := server.open(t)
	b.clients.endpoint = ""
	class := providerkit.ClassProduction
	sent, err := json.Marshal(b.envSyncSchedule(class, b.clients.EnvSyncJob(class)))
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
	if schedule.HTTPTarget.URI != "https://run.googleapis.com/v2/projects/acme-prod/locations/europe-west1/jobs/ocel-production-envsync:run" ||
		schedule.HTTPTarget.HTTPMethod != http.MethodPost {
		t.Errorf("the schedule calls %s %s, want POST on the job's :run", schedule.HTTPTarget.HTTPMethod, schedule.HTTPTarget.URI)
	}
	if schedule.HTTPTarget.OauthToken.ServiceAccountEmail != b.clients.EnvSyncInvokerEmail(class) || schedule.HTTPTarget.OidcToken != nil {
		t.Errorf("the schedule signs in as %+v, want an OAuth token for the invoker: a googleapis.com target takes OAuth, not OIDC", schedule.HTTPTarget)
	}
}

func TestTheScheduleIsStoodAndMendedThroughCloudScheduler(t *testing.T) {
	t.Parallel()
	server := standingSync()
	b := server.open(t)
	ctx := context.Background()
	class := providerkit.ClassProduction
	name := b.clients.EnvSyncJob(class)
	path := jobPath(b.clients, name)

	if err := b.makeSchedule(ctx, class, name); err != nil {
		t.Fatalf("makeSchedule() = %v", err)
	}
	var schedule heldSchedule
	if !server.held(server.schedules, path, &schedule) {
		t.Fatalf("no schedule stands at %s", path)
	}
	if !strings.HasPrefix(schedule.HTTPTarget.URI, b.clients.endpoint+"/v2/") {
		t.Errorf("the emulated schedule calls %s, want the emulator's Cloud Run rather than Google's", schedule.HTTPTarget.URI)
	}
	if stands, err := b.scheduleStands(ctx, class, name); err != nil || !stands.held || stands.mends != "" {
		t.Errorf("scheduleStands() = %+v, %v, want it standing with nothing to mend", stands, err)
	}

	server.mu.Lock()
	server.schedules[path] = json.RawMessage(strings.Replace(string(server.schedules[path]), "* * * * *", "*/5 * * * *", 1))
	server.mu.Unlock()
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

	if err := b.takeSchedule(ctx, name); err != nil {
		t.Fatalf("takeSchedule() = %v", err)
	}
	if err := b.takeSchedule(ctx, name); err != nil {
		t.Errorf("takeSchedule() of one already gone = %v, want nothing to do", err)
	}
}

func TestRemovingAClassStopsTheScheduleAndTheJobBeforeTheKeyAndTheAccounts(t *testing.T) {
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
	schedule := at(item{Kind: KindSchedule, Name: names.EnvSyncJob(class)})
	job := at(item{Kind: KindJob, Name: names.EnvSyncJob(class)})
	for _, later := range []item{
		{Kind: KindKey, Name: string(class)},
		{Kind: KindServiceAccount, Name: names.EnvSyncAccount(class)},
		{Kind: KindServiceAccount, Name: names.EnvSyncInvoker(class)},
		{Kind: KindServiceAccount, Name: names.RuntimeAccount(class)},
		{Kind: KindRepository, Name: names.Repository(class)},
	} {
		if schedule < 0 || job < 0 || schedule > job || job > at(later) {
			t.Errorf("removal runs %v, want the schedule, then the job, then %s", order, later.ID())
		}
	}
	if last := order[len(order)-1]; last != (item{Kind: KindBucket, Name: names.Bucket(class)}).ID() {
		t.Errorf("removal ends on %s, want the bucket holding the stamp: a removal stopped part way reads as unfinished only while it stands", last)
	}
	if len(order) != len(bootstrapItems(names, class, false)) {
		t.Errorf("removal takes %d items, want every one of the %d the bootstrap stands", len(order), len(bootstrapItems(names, class, false)))
	}
}

func TestABootstrapChecksThePermissionsTheEnvSyncerNeeds(t *testing.T) {
	t.Parallel()
	for _, permission := range []string{
		"run.jobs.create", "run.jobs.get", "run.jobs.update", "run.jobs.delete",
		"run.jobs.getIamPolicy", "run.jobs.setIamPolicy",
		"cloudscheduler.jobs.create", "cloudscheduler.jobs.get", "cloudscheduler.jobs.update", "cloudscheduler.jobs.delete",
		"iam.serviceAccounts.actAs", "artifactregistry.repositories.uploadArtifacts",
	} {
		if !slices.Contains(bootstrapPermissions, permission) {
			t.Errorf("a bootstrap does not check %s, and the apply would fail standing the env syncer", permission)
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

func TestAScheduleStartingTheJobAsAnotherAccountIsMended(t *testing.T) {
	t.Parallel()
	b := bootstrapper{clients: &clients{Names: Names{namespace: "ocel", project: "acme-prod"}, region: "europe-west1"}}
	class := providerkit.ClassProduction
	want := b.envSyncSchedule(class, b.clients.EnvSyncJob(class))
	held := func(email string) *cloudscheduler.Job {
		copied := *want
		target := *want.HttpTarget
		target.OauthToken = nil
		if email != "" {
			target.OauthToken = &cloudscheduler.OAuthToken{ServiceAccountEmail: email}
		}
		copied.HttpTarget = &target
		return &copied
	}
	if !sameSchedule(held(b.clients.EnvSyncInvokerEmail(class)), want, false) {
		t.Error("the schedule this bootstrap stood reads as another")
	}
	if sameSchedule(held("someone@acme-prod.iam.gserviceaccount.com"), want, false) {
		t.Error("a schedule starting the job as another account reads as current, and that account may start what it likes")
	}
	if sameSchedule(held(""), want, false) {
		t.Error("a schedule that signs in as nobody reads as current, and Cloud Run refuses every start it makes")
	}
	if !sameSchedule(held(""), want, true) {
		t.Error("under the emulator, which keeps no OAuth token on a schedule, the schedule this bootstrap stood reads as another")
	}
}
