package deploy

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/aws/provider/compute/worker"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/cloudfront"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/functionnetwork"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/sharedstack"
)

func functionNetworkStacks(t *testing.T, apps ...string) (*Stacks, *mockedEngine, records.Store) {
	t.Helper()
	cfg := routedConfig(t, cloudfront.Kind)
	cfg.ArtifactRoot = siblingAppRoot(t, apps...)
	cfg.Env = "prod"
	cfg.Tier = environment.TierProduction
	cfg.ArtifactBucket = "artifacts-bucket"
	cfg.StateTable = "ocel-state"
	cfg.StateTableARN = "arn:aws:dynamodb:us-east-1:123456789012:table/ocel-state"
	cfg.BackendURL = "s3://ocel-state/shop"
	cfg.Passphrase = "a-passphrase"
	cfg.PulumiProject = "ocel-shop"
	cfg.Region = "us-east-1"
	cfg.ImageOptimizerURL = ""
	cfg.Objects = &fakeArtifactStore{exists: map[string]bool{}}
	cfg.EdgeAccessKeyID, cfg.EdgeSecretKey = "AKIAEDGE", "edge-secret"
	store := fake.NewKeyValues()
	cfg.KeyValues = store

	outputs := siblingAppOutputs(apps...)
	for key, value := range functionNetworkOutputs() {
		outputs[key] = value
	}
	for key, value := range newWorkerOutputs(map[string]string{"media": "ocel-app-shop-prod-web-media"}) {
		outputs[key] = value
	}
	cloud := &functionNetworkCloud{inputRecorder: &inputRecorder{}, vpcCIDR: "172.31.0.0/16", subnets: defaultVPCSubnets()}
	cfg.Network = cloud
	engine := &mockedEngine{outputs: outputs, mocks: cloud}
	return stacksWith(cfg, engine), engine, store
}

func usingPostgres(t *testing.T, app string) provider.StackSpec {
	t.Helper()
	spec := siblingAppSpec(t, app)
	spec.App.Grants = []provider.Binding{{Type: provider.BindingPostgres, Name: "db--main"}}
	return spec
}

func networkStackName() string {
	return functionnetwork.StackRef(environment.TierProduction).Name.String()
}

func TestTheFirstAppThatReachesAPostgresProvisionsTheFunctionNetworkAndTheLastTakesItDown(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	stacks, engine, store := functionNetworkStacks(t, "web", "admin")
	web, admin := usingPostgres(t, "web"), usingPostgres(t, "admin")

	said := &recordingProgress{}
	if _, err := stacks.Provision(ctx, web, said); err != nil {
		t.Fatalf("Provision(web) = %v", err)
	}
	if ran := engine.stacks(); len(ran) != 2 || ran[0] != networkStackName() || ran[1] != web.Ref.Name.String() {
		t.Fatalf("the first deploy ran %v, want the function network before the app: the app's functions need its subnets", ran)
	}
	if !slices.ContainsFunc(said.reported(), func(line string) bool { return strings.Contains(line, "several minutes") }) {
		t.Errorf("progress = %v, want a warning that the first attach is slow", said.reported())
	}

	if _, err := stacks.Provision(ctx, admin, progress.Discard()); err != nil {
		t.Fatalf("Provision(admin) = %v", err)
	}
	if ran := engine.stacks(); len(ran) != 3 {
		t.Fatalf("the second deploy ran %v, want the network reused rather than provisioned again", ran)
	}
	recorded, present, err := stackrecords.ReadBootstrapPart(ctx, store, environment.TierProduction, functionnetwork.Part)
	if err != nil || !present || recorded.State != stackrecords.PartReady || recorded.AddedBy != web.Ref.Project || len(recorded.Resources) == 0 {
		t.Errorf("function-network record = %+v, %v, %v, want it ready, added by %s, with its resources", recorded, present, err, web.Ref.Project)
	}

	if err := stacks.Destroy(ctx, provider.StackDestroy{Ref: web.Ref}, progress.Discard()); err != nil {
		t.Fatalf("Destroy(web) = %v", err)
	}
	if torn := engine.torn(); len(torn) != 1 {
		t.Fatalf("destroying one of two apps tore down %v, want only its own: the other still runs in the network", torn)
	}
	if err := stacks.Destroy(ctx, provider.StackDestroy{Ref: admin.Ref}, progress.Discard()); err != nil {
		t.Fatalf("Destroy(admin) = %v", err)
	}
	torn := engine.torn()
	if len(torn) != 3 || torn[2] != networkStackName() {
		t.Fatalf("destroying the last app tore down %v, want the network to go after the app whose functions held its interfaces", torn)
	}
	if _, present, err := stackrecords.Read(ctx, store, environment.TierProduction, functionnetwork.Slug, functionnetwork.StackRef(environment.TierProduction).Name); err != nil || present {
		t.Errorf("the function network is still recorded (present %v, err %v) after its last consumer left", present, err)
	}
	if _, present, err := stackrecords.ReadBootstrapPart(ctx, store, environment.TierProduction, functionnetwork.Part); err != nil || present {
		t.Errorf("function-network record present %v (%v) after its last consumer left, want it forgotten", present, err)
	}
}

func TestAnAppThatNeedsNoVPCOnlyResourceNeverProvisionsTheFunctionNetwork(t *testing.T) {
	t.Parallel()

	stacks, engine, _ := functionNetworkStacks(t, "web")
	web := siblingAppSpec(t, "web")
	web.App.Grants = []provider.Binding{{Type: provider.BindingBucket, Name: "bucket--uploads"}}

	if _, err := stacks.Provision(context.Background(), web, progress.Discard()); err != nil {
		t.Fatalf("Provision(web) = %v", err)
	}
	if ran := engine.stacks(); len(ran) != 1 {
		t.Errorf("the engine ran %v, want the app alone: a NAT gateway bills by the hour and nothing here needs one", ran)
	}
}

func TestADeployThatNoLongerNeedsTheFunctionNetworkLetsItGo(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	stacks, engine, _ := functionNetworkStacks(t, "web")
	web := usingPostgres(t, "web")
	if _, err := stacks.Provision(ctx, web, progress.Discard()); err != nil {
		t.Fatalf("Provision(web) = %v", err)
	}

	web.App.Grants = nil
	if _, err := stacks.Provision(ctx, web, progress.Discard()); err != nil {
		t.Fatalf("Provision(web) without the postgres = %v", err)
	}

	if torn := engine.torn(); len(torn) != 1 || torn[0] != networkStackName() {
		t.Errorf("torn down = %v, want the function network: the app that held it no longer reaches anything in the VPC", torn)
	}
	want := []string{"up " + networkStackName(), "up " + web.Ref.Name.String(), "up " + web.Ref.Name.String(), "destroy " + networkStackName()}
	if ran := engine.operations(); !slices.Equal(ran, want) {
		t.Errorf("operations = %v, want %v: the app's functions must leave the subnets before they go", ran, want)
	}
}

func TestWorkersThatNoLongerNeedTheFunctionNetworkLeaveItBeforeItGoes(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	stacks, engine, _ := functionNetworkStacks(t, "web")
	web := usingPostgresWithWorkers(t)
	if _, err := stacks.Provision(ctx, web, progress.Discard()); err != nil {
		t.Fatalf("Provision(web) = %v", err)
	}

	web.App.Grants = nil
	if _, err := stacks.Provision(ctx, web, progress.Discard()); err != nil {
		t.Fatalf("Provision(web) without the postgres = %v", err)
	}

	ran := engine.operations()
	workers := slices.Index(ran, "up "+worker.StackName("prod", "web").String())
	if workers < 0 {
		t.Fatalf("operations = %v, want the workers run", ran)
	}
	last := slices.Index(ran[workers+1:], "up "+worker.StackName("prod", "web").String()) + workers + 1
	gone := slices.Index(ran, "destroy "+networkStackName())
	if gone < 0 || last <= workers || gone < last {
		t.Errorf("operations = %v, want the network destroyed after the second workers run: the workers' functions hold its interfaces until then", ran)
	}
}

func TestAnAppIsRefusedWhileTheFunctionNetworkIsGoingDown(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	stacks, _, store := functionNetworkStacks(t, "web")
	leased, err := records.ReadOrEmpty(ctx, store, functionnetwork.Shared.LeaseAt(environment.TierProduction))
	if err != nil {
		t.Fatal(err)
	}
	if err := functionnetwork.Shared.WriteLease(ctx, store, leased, sharedstack.Lease{Destroying: true, DestroyingSince: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}

	_, err = stacks.Provision(ctx, usingPostgres(t, "web"), progress.Discard())

	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeBusy {
		t.Fatalf("Provision(web) = %v, want the busy refusal a network on its way down earns", err)
	}
	consumers := functionnetwork.Shared.ConsumersPrefix(environment.TierProduction)
	remaining, err := store.List(ctx, consumers.Partition, consumers.Path...)
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 0 {
		t.Errorf("%d consumers remain, want none: a refused claim must not keep the network up", len(remaining))
	}
}

func TestThePlanOfTheFirstAppThatReachesAPostgresShowsTheFunctionNetworkItRaises(t *testing.T) {
	t.Parallel()

	stacks, _, _ := functionNetworkStacks(t, "web")

	plan, err := stacks.Plan(context.Background(), usingPostgres(t, "web"), progress.Discard())
	if err != nil {
		t.Fatalf("Plan(web) = %v", err)
	}

	if len(plan.Groups) == 0 || plan.Groups[0].Name != networkStackName() || plan.Groups[0].Action != provider.ActionCreate || !plan.Groups[0].Slow {
		t.Fatalf("plan groups = %+v, want the function network first, created, and flagged slow: it bills by the hour and takes minutes", plan.Groups)
	}
}

func TestThePlanOfAnAppWhoseFunctionNetworkExistsShowsNoNewOne(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	stacks, _, _ := functionNetworkStacks(t, "web", "admin")
	if _, err := stacks.Provision(ctx, usingPostgres(t, "web"), progress.Discard()); err != nil {
		t.Fatalf("Provision(web) = %v", err)
	}

	plan, err := stacks.Plan(ctx, usingPostgres(t, "admin"), progress.Discard())
	if err != nil {
		t.Fatalf("Plan(admin) = %v", err)
	}

	for _, group := range plan.Groups {
		if group.Name == networkStackName() {
			t.Errorf("plan names the function network %+v, want it left out where it already exists", group)
		}
	}
}

func TestADeployWhoseWorkersFailLetsGoOfNothing(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	stacks, engine, _ := functionNetworkStacks(t, "web")
	if _, err := stacks.Provision(ctx, usingPostgres(t, "web"), progress.Discard()); err != nil {
		t.Fatalf("Provision(web) = %v", err)
	}

	web := usingPostgresWithWorkers(t)
	web.App.Grants = nil
	engine.upErr = func(stack string) error {
		if stack == worker.StackName("prod", "web").String() {
			return errors.New("the workers stack failed")
		}
		return nil
	}
	if _, err := stacks.Provision(ctx, web, progress.Discard()); err == nil {
		t.Fatal("Provision(web) = nil, want the workers' failure")
	}
	if ran := engine.operations(); slices.Contains(ran, "destroy "+networkStackName()) {
		t.Errorf("operations = %v, want the function network kept: a deploy that failed lets go of nothing it held", ran)
	}
}

func usingPostgresWithWorkers(t *testing.T) provider.StackSpec {
	t.Helper()
	web := usingPostgres(t, "web")
	web.App.Workers = []provider.WorkerSpec{{Name: "media"}}
	web.App.Functions[0].HasWorkerEntry = true
	return web
}

func requireDestroyedInOrder(t *testing.T, engine *mockedEngine, stacks ...string) {
	t.Helper()
	ran := engine.operations()
	at := -1
	for _, stack := range stacks {
		next := slices.Index(ran, "destroy "+stack)
		if next < 0 || next < at {
			t.Fatalf("operations = %v, want %v destroyed in that order: the function network goes only once no function holds its interfaces", ran, stacks)
		}
		at = next
	}
}

func TestTheWorkersOfAnAppHoldTheFunctionNetworkUntilRetirementTakesTheirStackDown(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	stacks, engine, store := functionNetworkStacks(t, "web")
	live := usingPostgresWithWorkers(t)
	if _, err := stacks.Provision(ctx, live, progress.Discard()); err != nil {
		t.Fatalf("Provision(web) = %v", err)
	}
	recordRelease(t, store, live.Ref)
	next := siblingAppSpec(t, "web")
	next.Ref.Name = naming.AppStack(live.Ref.Name.Env, "web", mustReleaseToken(t, "r00000009"))
	if _, err := stacks.Provision(ctx, next, progress.Discard()); err != nil {
		t.Fatalf("Provision(web) with no postgres and no workers = %v", err)
	}
	recordRelease(t, store, next.Ref)
	if err := stacks.Destroy(ctx, provider.StackDestroy{Ref: live.Ref}, progress.Discard()); err != nil {
		t.Fatalf("Destroy(the superseded release) = %v", err)
	}
	if torn := engine.torn(); slices.Contains(torn, networkStackName()) || slices.Contains(torn, worker.StackName("prod", "web").String()) {
		t.Fatalf("torn down = %v before any promotion stopped needing the workers, want their stack and its function network kept", torn)
	}

	if err := stacks.RetireWorkers(ctx, provider.LiveApps{Slug: "shop", Tier: environment.TierProduction, Env: "prod", Apps: []provider.LiveApp{{App: "web", Compute: provider.ComputeServerless}}}, progress.Discard()); err != nil {
		t.Fatalf("RetireWorkers() = %v", err)
	}

	requireDestroyedInOrder(t, engine, worker.StackName("prod", "web").String(), networkStackName())
}

func TestAnAppNoLongerDeployedLetsTheFunctionNetworkGoOnceItsWorkersAndLastReleaseAreGone(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	stacks, engine, store := functionNetworkStacks(t, "web")
	web := usingPostgresWithWorkers(t)
	if _, err := stacks.Provision(ctx, web, progress.Discard()); err != nil {
		t.Fatalf("Provision(web) = %v", err)
	}
	recordRelease(t, store, web.Ref)

	if err := stacks.RetireWorkers(ctx, provider.LiveApps{Slug: "shop", Tier: environment.TierProduction, Env: "prod"}, progress.Discard()); err != nil {
		t.Fatalf("RetireWorkers() = %v", err)
	}
	if slices.Contains(engine.torn(), networkStackName()) {
		t.Fatalf("torn down = %v, want the function network kept: the release kept for rollback still runs its functions in it", engine.torn())
	}
	if err := stacks.Destroy(ctx, provider.StackDestroy{Ref: web.Ref}, progress.Discard()); err != nil {
		t.Fatalf("Destroy(web) = %v", err)
	}

	requireDestroyedInOrder(t, engine, worker.StackName("prod", "web").String(), web.Ref.Name.String(), networkStackName())
}

func TestDestroyingTheOnlyReleaseOfAnAppTakesItsWorkersAndThenTheFunctionNetworkDown(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	stacks, engine, store := functionNetworkStacks(t, "web")
	web := usingPostgresWithWorkers(t)
	if _, err := stacks.Provision(ctx, web, progress.Discard()); err != nil {
		t.Fatalf("Provision(web) = %v", err)
	}
	recordRelease(t, store, web.Ref)

	if err := stacks.Destroy(ctx, provider.StackDestroy{Ref: web.Ref}, progress.Discard()); err != nil {
		t.Fatalf("Destroy(web) = %v", err)
	}

	requireDestroyedInOrder(t, engine, worker.StackName("prod", "web").String(), web.Ref.Name.String(), networkStackName())
}

func TestAFailedTakedownOfTheFunctionNetworkLeavesItsPartForTheRemovalItNames(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	stacks, engine, store := functionNetworkStacks(t, "web")
	web := usingPostgres(t, "web")
	if _, err := stacks.Provision(ctx, web, progress.Discard()); err != nil {
		t.Fatalf("Provision(web) = %v", err)
	}
	engine.downErr = failingOnce(networkStackName())
	web.App.Grants = nil
	_, err := stacks.Provision(ctx, web, progress.Discard())
	if err == nil || !strings.Contains(err.Error(), "ocel bootstrap production --remove "+functionnetwork.Part) {
		t.Fatalf("Provision(web) = %v, want the failed takedown reported with the command that takes it down again", err)
	}
	if _, present, err := stackrecords.ReadBootstrapPart(ctx, store, environment.TierProduction, functionnetwork.Part); err != nil || !present {
		t.Fatalf("function-network record present %v (%v) after a failed takedown, want it kept: its NAT gateway still bills", present, err)
	}

	if err := stacks.functionNetwork.Remove(ctx, environment.TierProduction, progress.Discard()); err != nil {
		t.Fatalf("functionnetwork.Remove() after the failed takedown = %v", err)
	}

	if torn := engine.torn(); !slices.Equal(torn, []string{networkStackName()}) {
		t.Errorf("torn down = %v, want the function network", torn)
	}
	if _, present, err := stackrecords.ReadBootstrapPart(ctx, store, environment.TierProduction, functionnetwork.Part); err != nil || present {
		t.Errorf("function-network record present %v (%v) after its removal, want it forgotten", present, err)
	}
}

func TestRemovingTheFunctionNetworkIsRefusedWhileAnAppRunsInIt(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	stacks, engine, _ := functionNetworkStacks(t, "web")
	web := usingPostgres(t, "web")
	if _, err := stacks.Provision(ctx, web, progress.Discard()); err != nil {
		t.Fatalf("Provision(web) = %v", err)
	}

	err := stacks.functionNetwork.Remove(ctx, environment.TierProduction, progress.Discard())

	var refused refusal.Refusal
	if !errors.As(err, &refused) || !strings.Contains(refused.Message, web.Ref.Project) {
		t.Fatalf("functionnetwork.Remove() while %s runs in it = %v, want a refusal naming it", web.Ref.Project, err)
	}
	if torn := engine.torn(); len(torn) != 0 {
		t.Errorf("a refused removal tore down %v, want nothing", torn)
	}
}

func TestAnAppWhoseFunctionNetworkFailsLeavesNoFunctionNetworkPartCreating(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	stacks, engine, store := functionNetworkStacks(t, "web")
	engine.upErr = func(stack string) error {
		if stack == networkStackName() {
			return errors.New("the elastic IP quota is spent")
		}
		return nil
	}

	if _, err := stacks.Provision(ctx, usingPostgres(t, "web"), progress.Discard()); err == nil {
		t.Fatal("Provision(web) succeeded, want the failed function network to fail it")
	}

	if recorded, present, err := stackrecords.ReadBootstrapPart(ctx, store, environment.TierProduction, functionnetwork.Part); err != nil || present {
		t.Errorf("function-network record = %+v, %v, %v, want no claim left creating", recorded, present, err)
	}
}

func TestAnAppReachingAPostgresAfterAFailedTakedownRepairsTheFunctionNetwork(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	stacks, engine, _ := functionNetworkStacks(t, "web", "admin")
	web := usingPostgres(t, "web")
	if _, err := stacks.Provision(ctx, web, progress.Discard()); err != nil {
		t.Fatalf("Provision(web) = %v", err)
	}
	engine.downErr = failingOnce(networkStackName())
	web.App.Grants = nil
	if _, err := stacks.Provision(ctx, web, progress.Discard()); err == nil {
		t.Fatal("Provision(web) = nil, want the failed takedown reported")
	}

	admin := usingPostgres(t, "admin")
	if _, err := stacks.Provision(ctx, admin, progress.Discard()); err != nil {
		t.Fatalf("Provision(admin) after the failed takedown = %v, want it to proceed rather than wait on a takedown nobody is running", err)
	}

	ran := engine.stacks()
	if len(ran) < 2 || ran[len(ran)-2] != networkStackName() || ran[len(ran)-1] != admin.Ref.Name.String() {
		t.Errorf("ran %v, want the function network run again before admin: the failed takedown may have removed part of it", ran)
	}
}

func TestTheFunctionNetworkIsDescribedAsAPartWithTheProjectsInItAndTheIdleCostOfItsGateway(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	stacks, _, store := functionNetworkStacks(t, "web")
	tier := environment.TierProduction
	if _, present, err := functionnetwork.ReadPart(ctx, store, "us-east-1", tier); err != nil || present {
		t.Fatalf("functionnetwork.ReadPart() before any app reached a postgres = %v, %v, want none", present, err)
	}
	web := usingPostgresWithWorkers(t)
	if _, err := stacks.Provision(ctx, web, progress.Discard()); err != nil {
		t.Fatalf("Provision(web) = %v", err)
	}

	part, present, err := functionnetwork.ReadPart(ctx, store, "us-east-1", tier)
	if err != nil || !present {
		t.Fatalf("functionnetwork.ReadPart() = %v, %v, want the function network", present, err)
	}
	if part.Name != functionnetwork.Part || !slices.Equal(part.UsedBy, []string{web.Ref.Project}) || !part.RetiresWithLastUser {
		t.Errorf("function network = %+v, want it named %s, used by %s once for its app and workers, and retiring with its last user", part, functionnetwork.Part, web.Ref.Project)
	}
	if !slices.ContainsFunc(part.Resources, func(resource provider.PartResource) bool { return resource.Kind == "AWS::EC2::NatGateway" }) {
		t.Errorf("function network resources = %v, want its NAT gateway", part.Resources)
	}
	priced := []string{}
	if part.Shape != nil {
		for _, shaped := range part.Shape.Resources {
			priced = append(priced, shaped.Type)
		}
	}
	if !slices.Contains(priced, "aws_nat_gateway") || !slices.Contains(priced, "aws_eip") {
		t.Errorf("function network priced as %v, want its NAT gateway and elastic IP: they bill by the hour whether or not a function runs", priced)
	}
}

func TestReclaimingAFailedDeploysReleaseKeepsTheFunctionNetworkItRaisedAndTheRetryReusesIt(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	stacks, engine, store := functionNetworkStacks(t, "web")
	web := usingPostgres(t, "web")
	if _, err := stacks.Provision(ctx, web, progress.Discard()); err != nil {
		t.Fatalf("Provision(the failing deploy's release) = %v", err)
	}

	if err := stacks.Destroy(ctx, provider.StackDestroy{Ref: web.Ref, KeepParts: true}, progress.Discard()); err != nil {
		t.Fatalf("Destroy(the failed deploy's release) = %v", err)
	}
	if torn := engine.torn(); len(torn) != 1 || torn[0] != web.Ref.Name.String() {
		t.Fatalf("reclaiming the failed deploy's release tore down %v, want only its own stack: the retry would raise the function network again", torn)
	}
	tier := environment.TierProduction
	if _, present, err := stackrecords.Read(ctx, store, tier, functionnetwork.Slug, functionnetwork.StackRef(tier).Name); err != nil || !present {
		t.Errorf("the function network is recorded %v (%v), want its stack record kept for the retry to find", present, err)
	}
	if recorded, present, err := stackrecords.ReadBootstrapPart(ctx, store, tier, functionnetwork.Part); err != nil || !present || recorded.State != stackrecords.PartReady {
		t.Errorf("function-network record = %+v, %v, %v, want it kept ready", recorded, present, err)
	}
	if users, err := functionnetwork.Shared.ListUsers(ctx, store, tier); err != nil || len(users) != 0 {
		t.Errorf("function network users = %v (%v), want none once the failed release went", users, err)
	}

	retry := web
	retry.Ref.Name = naming.AppStack("prod", "web", mustReleaseToken(t, "r00000009"))
	if _, err := stacks.Provision(ctx, retry, progress.Discard()); err != nil {
		t.Fatalf("Provision(the retry) = %v", err)
	}
	if ran := engine.stacks(); len(ran) != 3 || ran[2] != retry.Ref.Name.String() {
		t.Errorf("the retry ran %v, want only its own app stack: the function network the failed deploy raised is still there", ran)
	}
}
