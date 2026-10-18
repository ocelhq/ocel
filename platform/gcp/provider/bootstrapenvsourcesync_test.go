package gcp

import (
	"context"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"testing"

	"google.golang.org/api/cloudscheduler/v1"
	runv2 "google.golang.org/api/run/v2"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/grants"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/run"
)

var bothTiers = []environment.Tier{environment.TierProduction, environment.TierPreview}

func longestNames() names.Names {
	return names.New(provider.Namespace(strings.Repeat("a", names.MaxAccountID-len("-")-names.AccountHashLen)), "acme-prod")
}

func TestTheSyncAccountFitsTheLongestNamespaceAnAccountLeavesRoomFor(t *testing.T) {
	t.Parallel()
	named := longestNames()
	if err := named.Fit(); err != nil {
		t.Fatalf("fit() = %v", err)
	}
	accountID := regexp.MustCompile(`^[a-z]([-a-z0-9]*[a-z0-9])$`)
	for _, tier := range bothTiers {
		account := named.EnvSourceSyncAccount(tier)
		if len(account) < 6 || len(account) > names.MaxAccountID || !accountID.MatchString(account) {
			t.Errorf("%s is %d characters, and IAM takes an id of 6 to 30 lowercase letters, digits and dashes", account, len(account))
		}
		if !strings.HasPrefix(account, string(named.Namespace())+"-") {
			t.Errorf("%s does not start with the namespace, so two namespaces in one project would share it", account)
		}
		if service := named.EnvSourceSync(tier); len(service) > names.MaxServiceNameLength {
			t.Errorf("%s is %d characters and Cloud Run takes %d", service, len(service), names.MaxServiceNameLength)
		}
	}
}

func TestEachTierSyncsAsAnAccountOfItsOwnThatEveryReadNamesTheSame(t *testing.T) {
	t.Parallel()
	named := names.New("ocel", "acme-prod")
	production, preview := named.EnvSourceSyncAccount(environment.TierProduction), named.EnvSourceSyncAccount(environment.TierPreview)
	if production == preview {
		t.Errorf("both tiers sync as %s, and one role would then serve both tiers", production)
	}
	if again := (names.New("ocel", "acme-prod")).EnvSourceSyncAccount(environment.TierProduction); again != production {
		t.Errorf("the production sync account is %s once and %s again, and a survey could not find the account an apply made", production, again)
	}
	for _, tier := range bothTiers {
		if named.EnvSourceSyncAccount(tier) == named.AppAccount(tier, "shop", "production", "api") {
			t.Errorf("the %s sync runs as an app's account, and an app would then write every value", tier)
		}
	}
	if got, want := named.EnvSourceSync(environment.TierPreview), "ocel-preview-envsourcesync"; got != want {
		t.Errorf("EnvSourceSync(preview) = %q, want %q", got, want)
	}
	if got, want := named.EnvSourceSyncAccountEmail(environment.TierPreview), preview+"@acme-prod.iam.gserviceaccount.com"; got != want {
		t.Errorf("EnvSourceSyncAccountEmail(preview) = %q, want %q", got, want)
	}
}

func TestTheSyncAccountMayWriteThisDatabaseAndSealAndOpenUnderItsTierKeyAlone(t *testing.T) {
	t.Parallel()
	server := googletest.GrantedIAM()
	b := composed(bootstrap.Engine{Clients: server.Open(t)})
	read := bootstrap.Survey{Tier: environment.TierProduction, Names: b.Clients.Names}
	ctx := context.Background()
	name := b.Clients.EnvSourceSyncAccount(environment.TierProduction)

	if err := b.MakeAccount(ctx, read, name); err != nil {
		t.Fatalf("makeAccount() = %v", err)
	}
	if len(server.Created) != 1 || server.Created[0].AccountId != name ||
		server.Created[0].ServiceAccount.DisplayName != "ocel env source sync (ocel, production)" ||
		!strings.Contains(server.Created[0].ServiceAccount.Description, "env source sync") {
		t.Errorf("created %+v, want %s named for what it is and whose it is: its id is a hash", server.Created, name)
	}
	member := "serviceAccount:" + b.Clients.EnvSourceSyncAccountEmail(environment.TierProduction)
	members, condition := server.ProjectMembers(syncRecordsRole)
	if !slices.Contains(members, member) {
		t.Errorf("the project binds %v to %s, want the sync account: it writes what an env source has into the records", members, syncRecordsRole)
	}
	if condition == nil || !strings.Contains(condition.Expression, "projects/acme-prod/databases/ocel") {
		t.Errorf("the sync account's records role is conditioned on %+v, want this namespace's one database", condition)
	}
	key := "projects/acme-prod/locations/europe-west1/keyRings/ocel/cryptoKeys/production"
	for _, role := range []string{syncSealingRole, syncOpeningRole} {
		if granted := server.KeyMembers(key, role); !slices.Contains(granted, member) {
			t.Errorf("the production key binds %v to %s, want the sync account: it opens its credentials and seals what it copies", granted, role)
		}
	}
	if readers, _ := server.ProjectMembers(grants.RecordsRole); slices.Contains(readers, member) {
		t.Errorf("the sync account has an app's %s too, and one grant is what it needs", grants.RecordsRole)
	}

	found, err := b.AccountPresence(ctx, environment.TierProduction, name)
	if err != nil || !found.Present || found.Mends != "" {
		t.Errorf("accountPresence() = %+v, %v after the grants landed, want it present with nothing to mend", found, err)
	}

	if err := b.TakeAccount(ctx, environment.TierProduction, name); err != nil {
		t.Fatalf("takeAccount() = %v", err)
	}
	if writers, _ := server.ProjectMembers(syncRecordsRole); slices.Contains(writers, member) {
		t.Errorf("the project still binds the deleted sync account to %s", syncRecordsRole)
	}
	for _, role := range []string{syncSealingRole, syncOpeningRole} {
		if granted := server.KeyMembers(key, role); slices.Contains(granted, member) {
			t.Errorf("the key still binds the deleted sync account to %s", role)
		}
	}
}

func TestASyncAccountThatMayNotWriteIsSurveyedAsMendable(t *testing.T) {
	t.Parallel()
	b := composed(bootstrap.Engine{Clients: googletest.GrantedIAM().Open(t)})

	found, err := b.AccountPresence(context.Background(), environment.TierProduction, b.Clients.EnvSourceSyncAccount(environment.TierProduction))
	if err != nil || !found.Present || found.Mends != reasonUnwritable {
		t.Errorf("accountPresence() = %+v, %v, want it present and mended for the writes it lacks", found, err)
	}
}

func idsOf(items []bootstrap.Resource) []string {
	ids := make([]string, 0, len(items))
	for _, each := range items {
		ids = append(ids, each.ID())
	}
	return ids
}

func TestTheSyncServiceRunsTheEmbeddedSyncAsItsOwnAccountAndOnlyThatAccountMayCallIt(t *testing.T) {
	t.Parallel()
	server := newSyncServer()
	b := server.open(t)
	ctx := context.Background()
	tier := environment.TierProduction
	name := b.Clients.EnvSourceSync(tier)
	path := b.Clients.ServicePath(name)

	if err := makeService(ctx, b, tier, name); err != nil {
		t.Fatalf("makeService() = %v", err)
	}
	ref := syncImageRef(b.Clients, tier)
	if !slices.Equal(server.pushed, []string{ref}) {
		t.Errorf("pushed %v, want the sync image at %s", server.pushed, ref)
	}
	if !strings.HasPrefix(ref, "europe-west1-docker.pkg.dev/acme-prod/"+b.Clients.Repository(tier)+"/ocel-envsourcesync:") {
		t.Errorf("the sync image is %s, want it in the tier's own repository", ref)
	}
	service := server.service(path)
	if service == nil {
		t.Fatalf("no service exists at %s", path)
	}
	template := service.Template
	if len(template.Containers) != 1 || template.Containers[0].Image != ref {
		t.Fatalf("the service runs %+v, want the one image %s", template.Containers, ref)
	}
	if template.ServiceAccount != b.Clients.EnvSourceSyncAccountEmail(tier) {
		t.Errorf("the service runs as %q, want the sync's own account", template.ServiceAccount)
	}
	env := map[string]string{}
	for _, each := range template.Containers[0].Env {
		env[each.Name] = each.Value
	}
	if env["OCEL_INFRA_TIER"] != "production" || env["OCEL_NAMESPACE"] != "ocel" || env["OCEL_GCP_PROJECT"] != "acme-prod" || env["OCEL_GCP_REGION"] != "europe-west1" {
		t.Errorf("the service is given %v, want the tier, namespace, project and region the sync reads", env)
	}
	resources := template.Containers[0].Resources
	if !resources.CpuIdle {
		t.Error("the service bills by instance, want request-based billing: it bills only while a sync runs")
	}
	if resources.Limits["cpu"] != "0.08" || resources.Limits["memory"] != "128Mi" {
		t.Errorf("the service is limited to %v, want the smallest Cloud Run takes: 0.08 vCPU and 128Mi", resources.Limits)
	}
	if template.ExecutionEnvironment != "EXECUTION_ENVIRONMENT_GEN1" {
		t.Errorf("the service runs in %q, want the first generation: it alone takes under 1 vCPU and under 512Mi", template.ExecutionEnvironment)
	}
	if template.MaxInstanceRequestConcurrency != 1 {
		t.Errorf("the service takes %d requests at once, want 1: under 1 vCPU Cloud Run takes no more", template.MaxInstanceRequestConcurrency)
	}
	if template.Scaling.MinInstanceCount != 0 || template.Scaling.MaxInstanceCount != 1 {
		t.Errorf("the service scales %+v, want a floor of 0 and a ceiling of 1: nothing stays warm, and two syncs never race", template.Scaling)
	}
	if template.Timeout != "60s" {
		t.Errorf("a request may run %q, want 60s: the next minute's sync is the retry", template.Timeout)
	}
	if service.Ingress != "INGRESS_TRAFFIC_INTERNAL_ONLY" {
		t.Errorf("the service takes %q, want internal traffic alone: Cloud Scheduler in the same project counts as internal", service.Ingress)
	}
	if service.InvokerIamDisabled {
		t.Error("anyone may call the service, want a caller to need run.invoker")
	}

	policy := server.policy(path)
	member := "serviceAccount:" + b.Clients.EnvSourceSyncAccountEmail(tier)
	if policy == nil || len(policy.Bindings) != 1 || policy.Bindings[0].Role != "roles/run.invoker" || !slices.Equal(policy.Bindings[0].Members, []string{member}) {
		t.Errorf("the service's policy is %s, want %s alone with roles/run.invoker", encoded(t, policy), member)
	}

	found, err := servicePresence(ctx, b, tier, name)
	if err != nil || !found.Present || found.Mends != "" {
		t.Errorf("servicePresence() = %+v, %v, want it present with nothing to mend", found, err)
	}
}

func TestASyncServiceRunOrReachedOtherwiseIsMendedInPlace(t *testing.T) {
	t.Parallel()
	for drift, change := range map[string]func(*runv2.GoogleCloudRunV2Service){
		"a new provider embeds a new sync":                 func(s *runv2.GoogleCloudRunV2Service) { s.Template.Containers[0].Image += "0" },
		"instance-based billing charges every idle minute": func(s *runv2.GoogleCloudRunV2Service) { s.Template.Containers[0].Resources.CpuIdle = false },
		"a warm instance bills around the clock":           func(s *runv2.GoogleCloudRunV2Service) { s.Template.Scaling.MinInstanceCount = 1 },
		"a second instance races the first":                func(s *runv2.GoogleCloudRunV2Service) { s.Template.Scaling.MaxInstanceCount = 3 },
		"a bigger instance bills more":                     func(s *runv2.GoogleCloudRunV2Service) { s.Template.Containers[0].Resources.Limits["cpu"] = "1" },
		"anyone on the internet may reach it":              func(s *runv2.GoogleCloudRunV2Service) { s.Ingress = "INGRESS_TRAFFIC_ALL" },
		"anyone may call it":                               func(s *runv2.GoogleCloudRunV2Service) { s.InvokerIamDisabled = true },
		"a sync outlives its minute into the next":         func(s *runv2.GoogleCloudRunV2Service) { s.Template.Timeout = "300s" },
		"one instance takes many syncs at once":            func(s *runv2.GoogleCloudRunV2Service) { s.Template.MaxInstanceRequestConcurrency = 80 },
		"the second generation bills a larger floor": func(s *runv2.GoogleCloudRunV2Service) {
			s.Template.ExecutionEnvironment = "EXECUTION_ENVIRONMENT_GEN2"
		},
		"it runs as an account that may write another tier": func(s *runv2.GoogleCloudRunV2Service) {
			s.Template.ServiceAccount = "ocel-preview@acme-prod.iam.gserviceaccount.com"
		},
	} {
		t.Run(drift, func(t *testing.T) {
			t.Parallel()
			server := newSyncServer()
			b := server.open(t)
			ctx := context.Background()
			tier := environment.TierPreview
			name := b.Clients.EnvSourceSync(tier)
			path := b.Clients.ServicePath(name)
			if err := makeService(ctx, b, tier, name); err != nil {
				t.Fatal(err)
			}
			server.change(func() { change(server.services[path]) })

			found, err := servicePresence(ctx, b, tier, name)
			if err != nil || !found.Present || found.Mends != reasonServiceChanged {
				t.Fatalf("servicePresence() = %+v, %v, want it present and mended", found, err)
			}
			if err := makeService(ctx, b, tier, name); err != nil {
				t.Fatalf("makeService() = %v", err)
			}
			if !server.wasPatched(path) {
				t.Error("the mend did not update the service in place")
			}
			if found, err := servicePresence(ctx, b, tier, name); err != nil || found.Mends != "" {
				t.Errorf("servicePresence() after the mend = %+v, %v", found, err)
			}
		})
	}
}

func TestAMendedSyncServiceServesTheRevisionTheMendMade(t *testing.T) {
	t.Parallel()
	server := newSyncServer()
	b := server.open(t)
	ctx := context.Background()
	tier := environment.TierPreview
	name := b.Clients.EnvSourceSync(tier)
	path := b.Clients.ServicePath(name)
	if err := makeService(ctx, b, tier, name); err != nil {
		t.Fatal(err)
	}
	server.change(func() { server.services[path].Template.Containers[0].Image += "0" })

	if err := makeService(ctx, b, tier, name); err != nil {
		t.Fatalf("makeService() = %v", err)
	}

	var served, latest string
	server.change(func() {
		served, latest = run.ServedRevision(server.services[path]), run.RevisionName(server.services[path].LatestReadyRevision)
	})
	if served != latest {
		t.Errorf("the sync service serves %q, want %q, the revision the mend made: the sync a new provider embeds otherwise never runs", served, latest)
	}
}

func TestASyncServiceWhoseLatestRevisionServesNoTrafficIsMended(t *testing.T) {
	t.Parallel()
	server := newSyncServer()
	b := server.open(t)
	ctx := context.Background()
	tier := environment.TierPreview
	name := b.Clients.EnvSourceSync(tier)
	path := b.Clients.ServicePath(name)
	if err := makeService(ctx, b, tier, name); err != nil {
		t.Fatal(err)
	}
	server.change(func() {
		server.services[path].Traffic = []*runv2.GoogleCloudRunV2TrafficTarget{{Type: run.TrafficByRevision, Revision: "an-older-revision", Percent: 100}}
	})

	found, err := servicePresence(ctx, b, tier, name)
	if err != nil || found.Mends != reasonServiceChanged {
		t.Errorf("servicePresence() = %+v, %v, want it mended: the revision that serves is not the one this bootstrap deployed", found, err)
	}
}

func TestASyncServiceCloudRunReadsBackInAnotherNotationForTheSameCPUAndMemoryIsCurrent(t *testing.T) {
	t.Parallel()
	for _, limits := range []map[string]string{
		{"cpu": "80m", "memory": "128Mi"},
		{"cpu": "0.080", "memory": "134217728"},
		{"cpu": "8e-2", "memory": "131072Ki"},
	} {
		t.Run(limits["cpu"]+"/"+limits["memory"], func(t *testing.T) {
			t.Parallel()
			server := newSyncServer()
			b := server.open(t)
			ctx := context.Background()
			tier := environment.TierPreview
			name := b.Clients.EnvSourceSync(tier)
			if err := makeService(ctx, b, tier, name); err != nil {
				t.Fatal(err)
			}
			server.change(func() { server.services[b.Clients.ServicePath(name)].Template.Containers[0].Resources.Limits = limits })

			if found, err := servicePresence(ctx, b, tier, name); err != nil || !found.Present || found.Mends != "" {
				t.Errorf("servicePresence() over %v = %+v, %v, want it current: it is the 0.08 vCPU and 128Mi this bootstrap deploys", limits, found, err)
			}
		})
	}
}

func TestASyncServiceAnyoneButTheSyncAccountMayCallIsMended(t *testing.T) {
	t.Parallel()
	for caller, change := range map[string]func(*runv2.GoogleIamV1Policy){
		"anyone": func(p *runv2.GoogleIamV1Policy) {
			p.Bindings[0].Members = append([]string{"allUsers"}, p.Bindings[0].Members...)
		},
		"another account": func(p *runv2.GoogleIamV1Policy) {
			p.Bindings[0].Members = append(p.Bindings[0].Members, "serviceAccount:someone@acme-prod.iam.gserviceaccount.com")
		},
		"a group under a second binding": func(p *runv2.GoogleIamV1Policy) {
			p.Bindings = append(p.Bindings, &runv2.GoogleIamV1Binding{
				Role:      "roles/run.invoker",
				Members:   []string{"group:everyone@acme.example"},
				Condition: &runv2.GoogleTypeExpr{Expression: "request.time < timestamp('2100-01-01T00:00:00Z')"},
			})
		},
	} {
		t.Run(caller, func(t *testing.T) {
			t.Parallel()
			server := newSyncServer()
			b := server.open(t)
			ctx := context.Background()
			tier := environment.TierProduction
			name := b.Clients.EnvSourceSync(tier)
			path := b.Clients.ServicePath(name)
			if err := makeService(ctx, b, tier, name); err != nil {
				t.Fatal(err)
			}
			server.change(func() { change(server.policies[path]) })

			found, err := servicePresence(ctx, b, tier, name)
			if err != nil || !found.Present || found.Mends != reasonCallersChanged {
				t.Fatalf("servicePresence() = %+v, %v, want a service %s may call mended", found, err, caller)
			}
			if err := makeService(ctx, b, tier, name); err != nil {
				t.Fatalf("makeService() over one that exists = %v", err)
			}
			member := "serviceAccount:" + b.Clients.EnvSourceSyncAccountEmail(tier)
			policy := server.policy(path)
			if len(policy.Bindings) != 1 || policy.Bindings[0].Condition != nil || !slices.Equal(policy.Bindings[0].Members, []string{member}) {
				t.Errorf("the mended policy is %s, want %s alone", encoded(t, policy), member)
			}
			if found, err := servicePresence(ctx, b, tier, name); err != nil || found.Mends != "" {
				t.Errorf("servicePresence() after the mend = %+v, %v", found, err)
			}
		})
	}
}

func TestLettingTheSyncAccountCallTheServiceRetriesWhenGoogleAnswers412(t *testing.T) {
	t.Parallel()
	server := newSyncServer()
	server.policyChanges = 1
	b := server.open(t)
	tier := environment.TierProduction
	name := b.Clients.EnvSourceSync(tier)

	if err := letSyncCall(context.Background(), b, tier, name); err != nil {
		t.Fatalf("letSyncCall() after one 412 = %v, want the write retried", err)
	}
	member := "serviceAccount:" + b.Clients.EnvSourceSyncAccountEmail(tier)
	policy := server.policy(b.Clients.ServicePath(name))
	if policy == nil || len(policy.Bindings) != 1 || !slices.Equal(policy.Bindings[0].Members, []string{member}) {
		t.Errorf("the policy is %s, want %s alone", encoded(t, policy), member)
	}
}

func TestTakingTheSyncServiceTwiceIsNothingToDo(t *testing.T) {
	t.Parallel()
	server := newSyncServer()
	b := server.open(t)
	ctx := context.Background()
	name := b.Clients.EnvSourceSync(environment.TierProduction)
	if err := makeService(ctx, b, environment.TierProduction, name); err != nil {
		t.Fatal(err)
	}
	if err := takeService(ctx, b, name); err != nil {
		t.Fatalf("takeService() = %v", err)
	}
	if found, err := servicePresence(ctx, b, environment.TierProduction, name); err != nil || found.Present {
		t.Errorf("servicePresence() after takeService() = %+v, %v, want it gone", found, err)
	}
	if err := takeService(ctx, b, name); err != nil {
		t.Errorf("takeService() of one already gone = %v, want nothing to do", err)
	}
}

func TestTheScheduleCallsTheServiceEveryMinuteWithAnIDTokenForTheSyncAccount(t *testing.T) {
	t.Parallel()
	b := composed(bootstrap.Engine{Clients: &gcloud.Clients{Names: names.New("ocel", "acme-prod"), Region: "europe-west1"}})
	tier := environment.TierProduction
	name := b.Clients.EnvSourceSync(tier)
	schedule := syncJob(b, tier, name, servedAt(name))
	if schedule.Schedule != "* * * * *" {
		t.Errorf("the schedule fires on %q, want every minute", schedule.Schedule)
	}
	if schedule.HttpTarget.Uri != servedAt(name)+"/" || schedule.HttpTarget.HttpMethod != http.MethodPost {
		t.Errorf("the schedule calls %s %s, want POST on the service's root", schedule.HttpTarget.HttpMethod, schedule.HttpTarget.Uri)
	}
	if schedule.HttpTarget.OidcToken.ServiceAccountEmail != b.Clients.EnvSourceSyncAccountEmail(tier) || schedule.HttpTarget.OauthToken != nil {
		t.Errorf("the schedule logs in as %+v, want an ID token for the sync account: Cloud Run takes an ID token, not an OAuth one", schedule.HttpTarget)
	}
	if schedule.HttpTarget.OidcToken.Audience != servedAt(name) {
		t.Errorf("the ID token is for %q, want the service's URL, the audience Cloud Run checks", schedule.HttpTarget.OidcToken.Audience)
	}
	if schedule.RetryConfig == nil || schedule.RetryConfig.RetryCount != 0 || !slices.Contains(schedule.RetryConfig.ForceSendFields, "RetryCount") {
		t.Errorf("the schedule retries %+v, want an explicit 0: the next minute is the retry", schedule.RetryConfig)
	}
}

func TestTheScheduleIsCreatedAndMendedThroughCloudScheduler(t *testing.T) {
	t.Parallel()
	server := newSyncServer()
	b := server.open(t)
	ctx := context.Background()
	tier := environment.TierProduction
	name := b.Clients.EnvSourceSync(tier)
	path := schedulePath(b.Clients, name)

	if err := makeService(ctx, b, tier, name); err != nil {
		t.Fatal(err)
	}
	if err := makeSchedule(ctx, b, tier, name); err != nil {
		t.Fatalf("makeSchedule() = %v", err)
	}
	created := server.schedule(path)
	if created == nil || created.HttpTarget.Uri != servedAt(name)+"/" {
		t.Fatalf("the schedule is %s, want one calling the URL the service is at", encoded(t, created))
	}
	if found, err := schedulePresence(ctx, b, tier, name); err != nil || !found.Present || found.Mends != "" {
		t.Errorf("schedulePresence() = %+v, %v, want it present with nothing to mend", found, err)
	}

	server.change(func() { server.schedules[path].Schedule = "*/5 * * * *" })
	if found, err := schedulePresence(ctx, b, tier, name); err != nil || found.Mends != reasonScheduleChanged {
		t.Fatalf("schedulePresence() = %+v, %v, want a schedule edited by hand mended", found, err)
	}
	if err := makeSchedule(ctx, b, tier, name); err != nil {
		t.Fatalf("makeSchedule() over one that exists = %v", err)
	}
	if !server.wasPatched(path) {
		t.Error("the existing schedule was not patched")
	}
	if patched := server.schedule(path); patched.Name != path {
		t.Errorf("the patched schedule is named %q, want %q", patched.Name, path)
	}
	if found, err := schedulePresence(ctx, b, tier, name); err != nil || found.Mends != "" {
		t.Errorf("schedulePresence() after the mend = %+v, %v", found, err)
	}

	servicePath := b.Clients.ServicePath(name)
	server.change(func() { server.services[servicePath].Uri = "https://elsewhere.a.run.app" })
	if found, err := schedulePresence(ctx, b, tier, name); err != nil || found.Mends != reasonScheduleChanged {
		t.Errorf("schedulePresence() = %+v, %v, want a schedule calling a URL the service no longer answers on mended", found, err)
	}

	if err := takeSchedule(ctx, b.Clients, name); err != nil {
		t.Fatalf("takeSchedule() = %v", err)
	}
	if err := takeSchedule(ctx, b.Clients, name); err != nil {
		t.Errorf("takeSchedule() of one already gone = %v, want nothing to do", err)
	}
}

func TestAScheduleThatIsPausedDisabledOrFailedItsUpdateIsMendedToCallEveryMinuteAgain(t *testing.T) {
	t.Parallel()
	for _, state := range []string{"PAUSED", "DISABLED", "UPDATE_FAILED"} {
		t.Run(state, func(t *testing.T) {
			t.Parallel()
			server := newSyncServer()
			b := server.open(t)
			ctx := context.Background()
			tier := environment.TierProduction
			name := b.Clients.EnvSourceSync(tier)
			path := schedulePath(b.Clients, name)
			if err := makeService(ctx, b, tier, name); err != nil {
				t.Fatal(err)
			}
			if err := makeSchedule(ctx, b, tier, name); err != nil {
				t.Fatal(err)
			}

			server.change(func() { server.schedules[path].State = state })
			if found, err := schedulePresence(ctx, b, tier, name); err != nil || !found.Present || found.Mends != reasonScheduleStopped {
				t.Fatalf("schedulePresence() of a %s schedule = %+v, %v, want it mended: it never calls the sync", state, found, err)
			}
			if err := makeSchedule(ctx, b, tier, name); err != nil {
				t.Fatalf("makeSchedule() over a %s schedule = %v", state, err)
			}
			if mended := server.schedule(path); mended == nil || mended.State != "ENABLED" || mended.HttpTarget.Uri != servedAt(name)+"/" {
				t.Errorf("the mended schedule is %s, want it enabled and calling the service", encoded(t, mended))
			}
			if found, err := schedulePresence(ctx, b, tier, name); err != nil || found.Mends != "" {
				t.Errorf("schedulePresence() after the mend = %+v, %v, want nothing to mend", found, err)
			}
		})
	}
}

func TestAScheduleWithNoServiceToCallIsRefusedAndOneThatOutlivedItsServiceIsMended(t *testing.T) {
	t.Parallel()
	server := newSyncServer()
	b := server.open(t)
	ctx := context.Background()
	tier := environment.TierPreview
	name := b.Clients.EnvSourceSync(tier)

	if err := makeSchedule(ctx, b, tier, name); err == nil || !strings.Contains(err.Error(), name) {
		t.Errorf("makeSchedule() without the service = %v, want a refusal naming %s: there is no URL to call", err, name)
	}
	server.change(func() {
		server.schedules[schedulePath(b.Clients, name)] = &cloudscheduler.Job{Schedule: "* * * * *", HttpTarget: &cloudscheduler.HttpTarget{Uri: "https://gone.a.run.app/"}}
	})
	if found, err := schedulePresence(ctx, b, tier, name); err != nil || !found.Present || found.Mends != reasonScheduleChanged {
		t.Errorf("schedulePresence() = %+v, %v, want a schedule that outlived its service mended once the service exists again", found, err)
	}
}

func TestAScheduleCallingAsAnotherAccountIsMended(t *testing.T) {
	t.Parallel()
	b := composed(bootstrap.Engine{Clients: &gcloud.Clients{Names: names.New("ocel", "acme-prod"), Region: "europe-west1"}})
	tier := environment.TierProduction
	uri := servedAt(b.Clients.EnvSourceSync(tier))
	desired := syncJob(b, tier, b.Clients.EnvSourceSync(tier), uri)
	signedBy := func(email, audience string) *cloudscheduler.Job {
		copied := *desired
		target := *desired.HttpTarget
		target.OidcToken = nil
		if email != "" {
			target.OidcToken = &cloudscheduler.OidcToken{ServiceAccountEmail: email, Audience: audience}
		}
		copied.HttpTarget = &target
		return &copied
	}
	account := b.Clients.EnvSourceSyncAccountEmail(tier)
	if !sameSchedule(signedBy(account, uri), desired, false) {
		t.Error("the schedule this bootstrap created reads as another")
	}
	if sameSchedule(signedBy("someone@acme-prod.iam.gserviceaccount.com", uri), desired, false) {
		t.Error("a schedule calling the service as another account reads as current, and that account may call what it likes")
	}
	if sameSchedule(signedBy(account, "https://elsewhere.a.run.app"), desired, false) {
		t.Error("a schedule minting a token for another audience reads as current, and Cloud Run refuses every call it makes")
	}
	if sameSchedule(signedBy("", ""), desired, false) {
		t.Error("a schedule that logs in as nobody reads as current, and Cloud Run refuses every call it makes")
	}
	if !sameSchedule(signedBy("", ""), desired, true) {
		t.Error("under the emulator, which keeps no token on a schedule, the schedule this bootstrap created reads as another")
	}
}

func TestRemovingATierTakesEveryItemItProvisionsAndEndsOnTheBucketWithTheStamp(t *testing.T) {
	t.Parallel()
	names := names.New("ocel", "acme-prod")
	tier := environment.TierProduction
	read := bootstrap.Survey{Tier: tier, Names: names, Held: map[string]bool{}}
	for _, each := range bootstrap.Resources(names, tier, false) {
		read.Held[each.ID()] = true
	}

	var order []string
	for _, taking := range bootstrap.Removals(read) {
		order = append(order, taking.Resource.ID())
	}
	at := func(target bootstrap.Resource) int { return slices.Index(order, target.ID()) }
	if at(bootstrap.Resource{Kind: bootstrap.KindBucket, Name: names.StateBucket(tier)}) > at(bootstrap.Resource{Kind: bootstrap.KindBucket, Name: names.Bucket(tier)}) {
		t.Errorf("removal runs %v, want the state bucket before the bucket with the stamp", order)
	}
	if last := order[len(order)-1]; last != (bootstrap.Resource{Kind: bootstrap.KindBucket, Name: names.Bucket(tier)}).ID() {
		t.Errorf("removal ends on %s, want the bucket with the stamp: a removal stopped part way reads as unfinished only while the stamp exists", last)
	}
	if len(order) != len(bootstrap.Resources(names, tier, false)) {
		t.Errorf("removal takes %d items, want every one of the %d the bootstrap provisions", len(order), len(bootstrap.Resources(names, tier, false)))
	}
}

func TestABootstrapChecksThePermissionsTheEnvSourceSyncNeeds(t *testing.T) {
	t.Parallel()
	for _, permission := range []string{
		"run.services.create", "run.services.get", "run.services.update", "run.services.delete",
		"run.services.getIamPolicy", "run.services.setIamPolicy", "run.operations.get",
		"cloudscheduler.jobs.create", "cloudscheduler.jobs.get", "cloudscheduler.jobs.update", "cloudscheduler.jobs.delete",
		"cloudscheduler.jobs.enable",
		"iam.serviceAccounts.actAs",
		"artifactregistry.repositories.uploadArtifacts", "artifactregistry.repositories.downloadArtifacts",
	} {
		if !slices.Contains(bootstrap.BootstrapPermissions, permission) {
			t.Errorf("a bootstrap does not check %s, and the apply would fail at the env source sync", permission)
		}
	}
	if !slices.Contains(bootstrap.RolesFor(edge.PurposeBootstrap), "roles/cloudscheduler.admin") {
		t.Errorf("bootstrap credentials are granted %v, want roles/cloudscheduler.admin among them", bootstrap.RolesFor(edge.PurposeBootstrap))
	}
	for _, role := range []string{"roles/cloudscheduler.admin", "roles/run.admin", "roles/iam.serviceAccountUser", "roles/artifactregistry.writer"} {
		if !slices.Contains(bootstrap.RolesCovering(nil), role) {
			t.Errorf("a refusal over a missing permission names %v, want %s among them: it covers what the env source sync checks", bootstrap.RolesCovering(nil), role)
		}
	}
	if !slices.Contains(bootstrap.BootstrapAPIs, "cloudscheduler.googleapis.com") {
		t.Errorf("a bootstrap checks %v are on, want cloudscheduler.googleapis.com among them", bootstrap.BootstrapAPIs)
	}
}
