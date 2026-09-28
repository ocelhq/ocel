package gcp

import (
	"context"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"testing"

	"google.golang.org/api/cloudscheduler/v1"
	run "google.golang.org/api/run/v2"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
)

var bothTiers = []environment.Tier{environment.TierProduction, environment.TierPreview}

func longestNames() Names {
	return Names{namespace: provider.Namespace(strings.Repeat("a", maxAccountID-len("-"+string(longestTier)))), project: "acme-prod"}
}

func TestTheSyncAccountFitsTheLongestNamespaceTheWorkloadAccountLeavesRoomFor(t *testing.T) {
	t.Parallel()
	names := longestNames()
	if err := names.fit(); err != nil {
		t.Fatalf("fit() = %v", err)
	}
	accountID := regexp.MustCompile(`^[a-z]([-a-z0-9]*[a-z0-9])$`)
	for _, tier := range bothTiers {
		account := names.EnvSourceSyncAccount(tier)
		if len(account) < 6 || len(account) > maxAccountID || !accountID.MatchString(account) {
			t.Errorf("%s is %d characters, and IAM takes an id of 6 to 30 lowercase letters, digits and dashes", account, len(account))
		}
		if !strings.HasPrefix(account, string(names.namespace)+"-") {
			t.Errorf("%s does not start with the namespace, so two namespaces in one project would share it", account)
		}
		if service := names.EnvSourceSync(tier); len(service) > maxServiceName {
			t.Errorf("%s is %d characters and Cloud Run takes %d", service, len(service), maxServiceName)
		}
	}
}

func TestEachTierSyncsAsAnAccountOfItsOwnThatEveryReadNamesTheSame(t *testing.T) {
	t.Parallel()
	names := Names{namespace: "ocel", project: "acme-prod"}
	production, preview := names.EnvSourceSyncAccount(environment.TierProduction), names.EnvSourceSyncAccount(environment.TierPreview)
	if production == preview {
		t.Errorf("both tiers sync as %s, and one role would then serve both tiers", production)
	}
	if again := (Names{namespace: "ocel", project: "acme-prod"}).EnvSourceSyncAccount(environment.TierProduction); again != production {
		t.Errorf("the production sync account is %s once and %s again, and a survey could not find the account an apply made", production, again)
	}
	for _, tier := range bothTiers {
		if names.EnvSourceSyncAccount(tier) == names.WorkloadAccount(tier) {
			t.Errorf("the %s sync runs as the account every app runs as, and an app would then write every value", tier)
		}
	}
	if got, want := names.EnvSourceSync(environment.TierPreview), "ocel-preview-envsourcesync"; got != want {
		t.Errorf("EnvSourceSync(preview) = %q, want %q", got, want)
	}
	if got, want := names.EnvSourceSyncAccountEmail(environment.TierPreview), preview+"@acme-prod.iam.gserviceaccount.com"; got != want {
		t.Errorf("EnvSourceSyncAccountEmail(preview) = %q, want %q", got, want)
	}
}

func TestTheSyncAccountMayWriteThisDatabaseAndSealAndOpenUnderItsTierKeyAlone(t *testing.T) {
	t.Parallel()
	server := grantedIAM()
	b := bootstrap{clients: server.open(t)}
	read := survey{Tier: environment.TierProduction, Names: b.clients.Names}
	ctx := context.Background()
	name := b.clients.EnvSourceSyncAccount(environment.TierProduction)

	if err := b.makeAccount(ctx, read, name); err != nil {
		t.Fatalf("makeAccount() = %v", err)
	}
	if len(server.created) != 1 || server.created[0].AccountId != name ||
		server.created[0].ServiceAccount.DisplayName != "ocel env source sync (ocel, production)" ||
		!strings.Contains(server.created[0].ServiceAccount.Description, "env source sync") {
		t.Errorf("created %+v, want %s named for what it is and whose it is: its id is a hash", server.created, name)
	}
	member := "serviceAccount:" + b.clients.EnvSourceSyncAccountEmail(environment.TierProduction)
	members, condition := server.projectMembers(syncRecordsRole)
	if !slices.Contains(members, member) {
		t.Errorf("the project binds %v to %s, want the sync account: it writes what an env source has into the records", members, syncRecordsRole)
	}
	if condition == nil || !strings.Contains(condition.Expression, "projects/acme-prod/databases/ocel") {
		t.Errorf("the sync account's records role is conditioned on %+v, want this namespace's one database", condition)
	}
	key := "projects/acme-prod/locations/europe-west1/keyRings/ocel/cryptoKeys/production"
	for _, role := range []string{syncSealingRole, syncOpeningRole} {
		if granted := server.keyMembers(key, role); !slices.Contains(granted, member) {
			t.Errorf("the production key binds %v to %s, want the sync account: it opens its credentials and seals what it copies", granted, role)
		}
	}
	if readers, _ := server.projectMembers(workloadRecordsRole); slices.Contains(readers, member) {
		t.Errorf("the sync account has the workload's %s too, and one grant is what it needs", workloadRecordsRole)
	}

	found, err := b.accountPresence(ctx, environment.TierProduction, name)
	if err != nil || !found.present || found.mends != "" {
		t.Errorf("accountPresence() = %+v, %v after the grants landed, want it present with nothing to mend", found, err)
	}

	if err := b.takeAccount(ctx, environment.TierProduction, name); err != nil {
		t.Fatalf("takeAccount() = %v", err)
	}
	if writers, _ := server.projectMembers(syncRecordsRole); slices.Contains(writers, member) {
		t.Errorf("the project still binds the deleted sync account to %s", syncRecordsRole)
	}
	for _, role := range []string{syncSealingRole, syncOpeningRole} {
		if granted := server.keyMembers(key, role); slices.Contains(granted, member) {
			t.Errorf("the key still binds the deleted sync account to %s", role)
		}
	}
}

func TestASyncAccountThatMayNotWriteIsSurveyedAsMendable(t *testing.T) {
	t.Parallel()
	b := bootstrap{clients: grantedIAM().open(t)}

	found, err := b.accountPresence(context.Background(), environment.TierProduction, b.clients.EnvSourceSyncAccount(environment.TierProduction))
	if err != nil || !found.present || found.mends != reasonUnwritable {
		t.Errorf("accountPresence() = %+v, %v, want it present and mended for the writes it lacks", found, err)
	}
}

func idsOf(items []item) []string {
	ids := make([]string, 0, len(items))
	for _, each := range items {
		ids = append(ids, each.ID())
	}
	return ids
}

func TestEachTierBootstrapsItsEnvSourceSyncAfterItsKeyAndItsAccount(t *testing.T) {
	t.Parallel()
	names := Names{namespace: "ocel", project: "acme-prod"}
	for _, tier := range bothTiers {
		for _, emulated := range []bool{false, true} {
			ids := idsOf(bootstrapItems(names, tier, emulated))
			at := func(target item) int { return slices.Index(ids, target.ID()) }
			key := at(item{Kind: KindKey, Name: string(tier)})
			account := at(item{Kind: KindServiceAccount, Name: names.EnvSourceSyncAccount(tier)})
			service := at(item{Kind: KindService, Name: names.EnvSourceSync(tier)})
			schedule := at(item{Kind: KindSchedule, Name: names.EnvSourceSync(tier)})
			if account < 0 || service < 0 || schedule < 0 {
				t.Fatalf("a %s bootstrap (emulated=%t) provisions %v, want the sync account, service and schedule among them", tier, emulated, ids)
			}
			if service < key || service < account || schedule < service {
				t.Errorf("a %s bootstrap (emulated=%t) provisions %v, want the key and the account before the service, and the service before the schedule that calls it", tier, emulated, ids)
			}
			if repository := at(item{Kind: KindRepository, Name: names.Repository(tier)}); !emulated && service < repository {
				t.Errorf("a %s bootstrap provisions %v, want the repository the service's image is pushed to before the service", tier, ids)
			}
		}
	}
}

func TestTheSyncServiceRunsTheEmbeddedSyncAsItsOwnAccountAndOnlyThatAccountMayCallIt(t *testing.T) {
	t.Parallel()
	server := newSyncServer()
	b := server.open(t)
	ctx := context.Background()
	tier := environment.TierProduction
	name := b.clients.EnvSourceSync(tier)
	path := b.clients.servicePath(name)

	if err := b.makeService(ctx, tier, name); err != nil {
		t.Fatalf("makeService() = %v", err)
	}
	ref := syncImageRef(b.clients, tier)
	if !slices.Equal(server.pushed, []string{ref}) {
		t.Errorf("pushed %v, want the sync image at %s", server.pushed, ref)
	}
	if !strings.HasPrefix(ref, "europe-west1-docker.pkg.dev/acme-prod/"+b.clients.Repository(tier)+"/ocel-envsourcesync:") {
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
	if template.ServiceAccount != b.clients.EnvSourceSyncAccountEmail(tier) {
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
	member := "serviceAccount:" + b.clients.EnvSourceSyncAccountEmail(tier)
	if policy == nil || len(policy.Bindings) != 1 || policy.Bindings[0].Role != "roles/run.invoker" || !slices.Equal(policy.Bindings[0].Members, []string{member}) {
		t.Errorf("the service's policy is %s, want %s alone with roles/run.invoker", encoded(t, policy), member)
	}

	found, err := b.servicePresence(ctx, tier, name)
	if err != nil || !found.present || found.mends != "" {
		t.Errorf("servicePresence() = %+v, %v, want it present with nothing to mend", found, err)
	}
}

func TestASyncServiceRunOrReachedOtherwiseIsMendedInPlace(t *testing.T) {
	t.Parallel()
	for drift, change := range map[string]func(*run.GoogleCloudRunV2Service){
		"a new provider embeds a new sync":                 func(s *run.GoogleCloudRunV2Service) { s.Template.Containers[0].Image += "0" },
		"instance-based billing charges every idle minute": func(s *run.GoogleCloudRunV2Service) { s.Template.Containers[0].Resources.CpuIdle = false },
		"a warm instance bills around the clock":           func(s *run.GoogleCloudRunV2Service) { s.Template.Scaling.MinInstanceCount = 1 },
		"a second instance races the first":                func(s *run.GoogleCloudRunV2Service) { s.Template.Scaling.MaxInstanceCount = 3 },
		"a bigger instance bills more":                     func(s *run.GoogleCloudRunV2Service) { s.Template.Containers[0].Resources.Limits["cpu"] = "1" },
		"anyone on the internet may reach it":              func(s *run.GoogleCloudRunV2Service) { s.Ingress = "INGRESS_TRAFFIC_ALL" },
		"anyone may call it":                               func(s *run.GoogleCloudRunV2Service) { s.InvokerIamDisabled = true },
		"a sync outlives its minute into the next":         func(s *run.GoogleCloudRunV2Service) { s.Template.Timeout = "300s" },
		"one instance takes many syncs at once":            func(s *run.GoogleCloudRunV2Service) { s.Template.MaxInstanceRequestConcurrency = 80 },
		"the second generation bills a larger floor": func(s *run.GoogleCloudRunV2Service) {
			s.Template.ExecutionEnvironment = "EXECUTION_ENVIRONMENT_GEN2"
		},
		"it runs as an account that may write another tier": func(s *run.GoogleCloudRunV2Service) {
			s.Template.ServiceAccount = "ocel-preview@acme-prod.iam.gserviceaccount.com"
		},
	} {
		t.Run(drift, func(t *testing.T) {
			t.Parallel()
			server := newSyncServer()
			b := server.open(t)
			ctx := context.Background()
			tier := environment.TierPreview
			name := b.clients.EnvSourceSync(tier)
			path := b.clients.servicePath(name)
			if err := b.makeService(ctx, tier, name); err != nil {
				t.Fatal(err)
			}
			server.change(func() { change(server.services[path]) })

			found, err := b.servicePresence(ctx, tier, name)
			if err != nil || !found.present || found.mends != reasonServiceChanged {
				t.Fatalf("servicePresence() = %+v, %v, want it present and mended", found, err)
			}
			if err := b.mend(ctx, survey{Tier: tier, Names: b.clients.Names}, item{Kind: KindService, Name: name}); err != nil {
				t.Fatalf("mend() = %v", err)
			}
			if !server.wasPatched(path) {
				t.Error("the mend did not update the service in place")
			}
			if found, err := b.servicePresence(ctx, tier, name); err != nil || found.mends != "" {
				t.Errorf("servicePresence() after the mend = %+v, %v", found, err)
			}
		})
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
			name := b.clients.EnvSourceSync(tier)
			if err := b.makeService(ctx, tier, name); err != nil {
				t.Fatal(err)
			}
			server.change(func() { server.services[b.clients.servicePath(name)].Template.Containers[0].Resources.Limits = limits })

			if found, err := b.servicePresence(ctx, tier, name); err != nil || !found.present || found.mends != "" {
				t.Errorf("servicePresence() over %v = %+v, %v, want it current: it is the 0.08 vCPU and 128Mi this bootstrap deploys", limits, found, err)
			}
		})
	}
	for _, limits := range []map[string]string{
		{"cpu": "81m", "memory": "128Mi"},
		{"cpu": "0.08", "memory": "128M"},
		{"cpu": "0.08"},
		{"cpu": "0.08", "memory": "128Mi", "nvidia.com/gpu": "1"},
		{"cpu": "a lot", "memory": "128Mi"},
	} {
		if sameLimits(limits, map[string]string{"cpu": "0.08", "memory": "128Mi"}) {
			t.Errorf("sameLimits(%v) reads as the 0.08 vCPU and 128Mi this bootstrap deploys", limits)
		}
	}
}

func TestASyncServiceCloudRunReadsBackWithTheSameTimeoutInAnotherNotationIsCurrent(t *testing.T) {
	t.Parallel()
	for _, timeout := range []string{"60s", "60.000s", "1m0s"} {
		if !sameTimeout(timeout, "60s") {
			t.Errorf("sameTimeout(%q) reads as another timeout than the 60s this bootstrap deploys", timeout)
		}
	}
	for _, timeout := range []string{"61s", "", "a minute"} {
		if sameTimeout(timeout, "60s") {
			t.Errorf("sameTimeout(%q) reads as the 60s this bootstrap deploys", timeout)
		}
	}
}

func TestASyncServiceAnyoneButTheSyncAccountMayCallIsMended(t *testing.T) {
	t.Parallel()
	for caller, change := range map[string]func(*run.GoogleIamV1Policy){
		"anyone": func(p *run.GoogleIamV1Policy) {
			p.Bindings[0].Members = append([]string{"allUsers"}, p.Bindings[0].Members...)
		},
		"another account": func(p *run.GoogleIamV1Policy) {
			p.Bindings[0].Members = append(p.Bindings[0].Members, "serviceAccount:someone@acme-prod.iam.gserviceaccount.com")
		},
		"a group under a second binding": func(p *run.GoogleIamV1Policy) {
			p.Bindings = append(p.Bindings, &run.GoogleIamV1Binding{
				Role:      "roles/run.invoker",
				Members:   []string{"group:everyone@acme.example"},
				Condition: &run.GoogleTypeExpr{Expression: "request.time < timestamp('2100-01-01T00:00:00Z')"},
			})
		},
	} {
		t.Run(caller, func(t *testing.T) {
			t.Parallel()
			server := newSyncServer()
			b := server.open(t)
			ctx := context.Background()
			tier := environment.TierProduction
			name := b.clients.EnvSourceSync(tier)
			path := b.clients.servicePath(name)
			if err := b.makeService(ctx, tier, name); err != nil {
				t.Fatal(err)
			}
			server.change(func() { change(server.policies[path]) })

			found, err := b.servicePresence(ctx, tier, name)
			if err != nil || !found.present || found.mends != reasonCallersChanged {
				t.Fatalf("servicePresence() = %+v, %v, want a service %s may call mended", found, err, caller)
			}
			if err := b.makeService(ctx, tier, name); err != nil {
				t.Fatalf("makeService() over one that exists = %v", err)
			}
			member := "serviceAccount:" + b.clients.EnvSourceSyncAccountEmail(tier)
			policy := server.policy(path)
			if len(policy.Bindings) != 1 || policy.Bindings[0].Condition != nil || !slices.Equal(policy.Bindings[0].Members, []string{member}) {
				t.Errorf("the mended policy is %s, want %s alone", encoded(t, policy), member)
			}
			if found, err := b.servicePresence(ctx, tier, name); err != nil || found.mends != "" {
				t.Errorf("servicePresence() after the mend = %+v, %v", found, err)
			}
		})
	}
}

func TestTakingTheSyncServiceTwiceIsNothingToDo(t *testing.T) {
	t.Parallel()
	server := newSyncServer()
	b := server.open(t)
	ctx := context.Background()
	name := b.clients.EnvSourceSync(environment.TierProduction)
	if err := b.makeService(ctx, environment.TierProduction, name); err != nil {
		t.Fatal(err)
	}
	if err := b.takeService(ctx, name); err != nil {
		t.Fatalf("takeService() = %v", err)
	}
	if found, err := b.servicePresence(ctx, environment.TierProduction, name); err != nil || found.present {
		t.Errorf("servicePresence() after takeService() = %+v, %v, want it gone", found, err)
	}
	if err := b.takeService(ctx, name); err != nil {
		t.Errorf("takeService() of one already gone = %v, want nothing to do", err)
	}
}

func TestTheScheduleCallsTheServiceEveryMinuteWithAnIDTokenForTheSyncAccount(t *testing.T) {
	t.Parallel()
	b := bootstrap{clients: &clients{Names: Names{namespace: "ocel", project: "acme-prod"}, region: "europe-west1"}}
	tier := environment.TierProduction
	name := b.clients.EnvSourceSync(tier)
	schedule := b.syncSchedule(tier, name, servedAt(name))
	if schedule.Schedule != "* * * * *" {
		t.Errorf("the schedule fires on %q, want every minute", schedule.Schedule)
	}
	if schedule.HttpTarget.Uri != servedAt(name)+"/" || schedule.HttpTarget.HttpMethod != http.MethodPost {
		t.Errorf("the schedule calls %s %s, want POST on the service's root", schedule.HttpTarget.HttpMethod, schedule.HttpTarget.Uri)
	}
	if schedule.HttpTarget.OidcToken.ServiceAccountEmail != b.clients.EnvSourceSyncAccountEmail(tier) || schedule.HttpTarget.OauthToken != nil {
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
	name := b.clients.EnvSourceSync(tier)
	path := schedulePath(b.clients, name)

	if err := b.makeService(ctx, tier, name); err != nil {
		t.Fatal(err)
	}
	if err := b.makeSchedule(ctx, tier, name); err != nil {
		t.Fatalf("makeSchedule() = %v", err)
	}
	created := server.schedule(path)
	if created == nil || created.HttpTarget.Uri != servedAt(name)+"/" {
		t.Fatalf("the schedule is %s, want one calling the URL the service is at", encoded(t, created))
	}
	if found, err := b.schedulePresence(ctx, tier, name); err != nil || !found.present || found.mends != "" {
		t.Errorf("schedulePresence() = %+v, %v, want it present with nothing to mend", found, err)
	}

	server.change(func() { server.schedules[path].Schedule = "*/5 * * * *" })
	if found, err := b.schedulePresence(ctx, tier, name); err != nil || found.mends != reasonScheduleChanged {
		t.Fatalf("schedulePresence() = %+v, %v, want a schedule edited by hand mended", found, err)
	}
	if err := b.makeSchedule(ctx, tier, name); err != nil {
		t.Fatalf("makeSchedule() over one that exists = %v", err)
	}
	if !server.wasPatched(path) {
		t.Error("the existing schedule was not patched")
	}
	if patched := server.schedule(path); patched.Name != path {
		t.Errorf("the patched schedule is named %q, want %q", patched.Name, path)
	}
	if found, err := b.schedulePresence(ctx, tier, name); err != nil || found.mends != "" {
		t.Errorf("schedulePresence() after the mend = %+v, %v", found, err)
	}

	servicePath := b.clients.servicePath(name)
	server.change(func() { server.services[servicePath].Uri = "https://elsewhere.a.run.app" })
	if found, err := b.schedulePresence(ctx, tier, name); err != nil || found.mends != reasonScheduleChanged {
		t.Errorf("schedulePresence() = %+v, %v, want a schedule calling a URL the service no longer answers on mended", found, err)
	}

	if err := b.takeSchedule(ctx, name); err != nil {
		t.Fatalf("takeSchedule() = %v", err)
	}
	if err := b.takeSchedule(ctx, name); err != nil {
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
			name := b.clients.EnvSourceSync(tier)
			path := schedulePath(b.clients, name)
			if err := b.makeService(ctx, tier, name); err != nil {
				t.Fatal(err)
			}
			if err := b.makeSchedule(ctx, tier, name); err != nil {
				t.Fatal(err)
			}

			server.change(func() { server.schedules[path].State = state })
			if found, err := b.schedulePresence(ctx, tier, name); err != nil || !found.present || found.mends != reasonScheduleStopped {
				t.Fatalf("schedulePresence() of a %s schedule = %+v, %v, want it mended: it never calls the sync", state, found, err)
			}
			if err := b.makeSchedule(ctx, tier, name); err != nil {
				t.Fatalf("makeSchedule() over a %s schedule = %v", state, err)
			}
			if mended := server.schedule(path); mended == nil || mended.State != "ENABLED" || mended.HttpTarget.Uri != servedAt(name)+"/" {
				t.Errorf("the mended schedule is %s, want it enabled and calling the service", encoded(t, mended))
			}
			if found, err := b.schedulePresence(ctx, tier, name); err != nil || found.mends != "" {
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
	name := b.clients.EnvSourceSync(tier)

	if err := b.makeSchedule(ctx, tier, name); err == nil || !strings.Contains(err.Error(), name) {
		t.Errorf("makeSchedule() without the service = %v, want a refusal naming %s: there is no URL to call", err, name)
	}
	server.change(func() {
		server.schedules[schedulePath(b.clients, name)] = &cloudscheduler.Job{Schedule: "* * * * *", HttpTarget: &cloudscheduler.HttpTarget{Uri: "https://gone.a.run.app/"}}
	})
	if found, err := b.schedulePresence(ctx, tier, name); err != nil || !found.present || found.mends != reasonScheduleChanged {
		t.Errorf("schedulePresence() = %+v, %v, want a schedule that outlived its service mended once the service exists again", found, err)
	}
}

func TestAScheduleCallingAsAnotherAccountIsMended(t *testing.T) {
	t.Parallel()
	b := bootstrap{clients: &clients{Names: Names{namespace: "ocel", project: "acme-prod"}, region: "europe-west1"}}
	tier := environment.TierProduction
	uri := servedAt(b.clients.EnvSourceSync(tier))
	desired := b.syncSchedule(tier, b.clients.EnvSourceSync(tier), uri)
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
	account := b.clients.EnvSourceSyncAccountEmail(tier)
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

func TestRemovingATierStopsTheScheduleAndTheServiceBeforeTheKeyAndTheAccounts(t *testing.T) {
	t.Parallel()
	names := Names{namespace: "ocel", project: "acme-prod"}
	tier := environment.TierProduction
	read := survey{Tier: tier, Names: names, present: map[string]bool{}}
	for _, each := range bootstrapItems(names, tier, false) {
		read.present[each.ID()] = true
	}

	var order []string
	for _, taking := range removals(read) {
		order = append(order, taking.item.ID())
	}
	at := func(target item) int { return slices.Index(order, target.ID()) }
	schedule := at(item{Kind: KindSchedule, Name: names.EnvSourceSync(tier)})
	service := at(item{Kind: KindService, Name: names.EnvSourceSync(tier)})
	for _, later := range []item{
		{Kind: KindKey, Name: string(tier)},
		{Kind: KindServiceAccount, Name: names.EnvSourceSyncAccount(tier)},
		{Kind: KindServiceAccount, Name: names.WorkloadAccount(tier)},
		{Kind: KindRepository, Name: names.Repository(tier)},
	} {
		if schedule < 0 || service < 0 || schedule > service || service > at(later) {
			t.Errorf("removal runs %v, want the schedule, then the service, then %s", order, later.ID())
		}
	}
	if at(item{Kind: KindBucket, Name: names.StateBucket(tier)}) > at(item{Kind: KindBucket, Name: names.Bucket(tier)}) {
		t.Errorf("removal runs %v, want the state bucket before the bucket with the stamp", order)
	}
	if last := order[len(order)-1]; last != (item{Kind: KindBucket, Name: names.Bucket(tier)}).ID() {
		t.Errorf("removal ends on %s, want the bucket with the stamp: a removal stopped part way reads as unfinished only while the stamp exists", last)
	}
	if len(order) != len(bootstrapItems(names, tier, false)) {
		t.Errorf("removal takes %d items, want every one of the %d the bootstrap provisions", len(order), len(bootstrapItems(names, tier, false)))
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
		if !slices.Contains(bootstrapPermissions, permission) {
			t.Errorf("a bootstrap does not check %s, and the apply would fail at the env source sync", permission)
		}
	}
	if !slices.Contains(rolesFor(edge.PurposeBootstrap), "roles/cloudscheduler.admin") {
		t.Errorf("bootstrap credentials are granted %v, want roles/cloudscheduler.admin among them", rolesFor(edge.PurposeBootstrap))
	}
	for _, role := range []string{"roles/cloudscheduler.admin", "roles/run.admin", "roles/iam.serviceAccountUser", "roles/artifactregistry.writer"} {
		if !slices.Contains(rolesCovering(nil), role) {
			t.Errorf("a refusal over a missing permission names %v, want %s among them: it covers what the env source sync checks", rolesCovering(nil), role)
		}
	}
	if slices.Contains(rolesFor(edge.PurposeDeploy), "roles/cloudscheduler.admin") {
		t.Error("deploy credentials are granted roles/cloudscheduler.admin, and a deploy creates no schedule")
	}
	if !slices.Contains(BootstrapAPIs, "cloudscheduler.googleapis.com") {
		t.Errorf("a bootstrap checks %v are on, want cloudscheduler.googleapis.com among them", BootstrapAPIs)
	}
}
