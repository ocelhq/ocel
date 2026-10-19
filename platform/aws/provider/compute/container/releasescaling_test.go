package container

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/applicationautoscaling"
	scalingtypes "github.com/aws/aws-sdk-go-v2/service/applicationautoscaling/types"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	elbv2 "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbv2types "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
)

const scalingNamespace = "ocel"

var fixtureCluster = awsports.ContainerClusterName(scalingNamespace, environment.TierProduction)

type capacity struct{ min, max int32 }

type scalingAccount struct {
	mu        sync.Mutex
	targets   map[string]capacity
	tagged    map[string]capacity
	scheduled map[string]string
	healthy   func(polls int) int
	polls     int

	described  int
	registered map[string]capacity
	deleted    []string
	updated    map[string]int32
}

func newScalingAccount() *scalingAccount {
	return &scalingAccount{
		targets:    map[string]capacity{},
		tagged:     map[string]capacity{},
		scheduled:  map[string]string{},
		registered: map[string]capacity{},
		updated:    map[string]int32{},
	}
}

func (a *scalingAccount) DescribeScalableTargets(_ context.Context, in *applicationautoscaling.DescribeScalableTargetsInput, _ ...func(*applicationautoscaling.Options)) (*applicationautoscaling.DescribeScalableTargetsOutput, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.described++
	if in.ServiceNamespace != scalingtypes.ServiceNamespaceEcs || in.ScalableDimension != scalingtypes.ScalableDimensionECSServiceDesiredCount {
		return nil, errors.New("described targets of something other than an ECS service's desired count")
	}
	if len(in.ResourceIds) == 0 || len(in.ResourceIds) > 50 {
		return nil, errors.New("described targets with no resource IDs or more than 50, which reads every target in the account or is refused")
	}
	out := &applicationautoscaling.DescribeScalableTargetsOutput{}
	for _, id := range in.ResourceIds {
		if held, found := a.targets[id]; found {
			out.ScalableTargets = append(out.ScalableTargets, scalingtypes.ScalableTarget{
				ResourceId:        aws.String(id),
				MinCapacity:       aws.Int32(held.min),
				MaxCapacity:       aws.Int32(held.max),
				ScalableTargetARN: aws.String("arn:aws:application-autoscaling:us-east-1:123456789012:scalable-target/" + id),
			})
		}
	}
	return out, nil
}

func (a *scalingAccount) RegisterScalableTarget(_ context.Context, in *applicationautoscaling.RegisterScalableTargetInput, _ ...func(*applicationautoscaling.Options)) (*applicationautoscaling.RegisterScalableTargetOutput, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	held := capacity{aws.ToInt32(in.MinCapacity), aws.ToInt32(in.MaxCapacity)}
	a.targets[aws.ToString(in.ResourceId)] = held
	a.registered[aws.ToString(in.ResourceId)] = held
	return &applicationautoscaling.RegisterScalableTargetOutput{}, nil
}

func (a *scalingAccount) PutScheduledAction(_ context.Context, in *applicationautoscaling.PutScheduledActionInput, _ ...func(*applicationautoscaling.Options)) (*applicationautoscaling.PutScheduledActionOutput, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	action := in.ScalableTargetAction
	if action == nil || aws.ToInt32(action.MinCapacity) != 0 || aws.ToInt32(action.MaxCapacity) != 0 {
		return nil, errors.New("scheduled an action that does not drop the target to zero")
	}
	a.scheduled[aws.ToString(in.ResourceId)+"/"+aws.ToString(in.ScheduledActionName)] = aws.ToString(in.Schedule)
	return &applicationautoscaling.PutScheduledActionOutput{}, nil
}

func (a *scalingAccount) DeleteScheduledAction(_ context.Context, in *applicationautoscaling.DeleteScheduledActionInput, _ ...func(*applicationautoscaling.Options)) (*applicationautoscaling.DeleteScheduledActionOutput, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	key := aws.ToString(in.ResourceId) + "/" + aws.ToString(in.ScheduledActionName)
	a.deleted = append(a.deleted, aws.ToString(in.ResourceId))
	if _, found := a.scheduled[key]; !found {
		return nil, &scalingtypes.ObjectNotFoundException{Message: aws.String("no such scheduled action")}
	}
	delete(a.scheduled, key)
	return &applicationautoscaling.DeleteScheduledActionOutput{}, nil
}

func (a *scalingAccount) ListTagsForResource(_ context.Context, in *applicationautoscaling.ListTagsForResourceInput, _ ...func(*applicationautoscaling.Options)) (*applicationautoscaling.ListTagsForResourceOutput, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	id := strings.TrimPrefix(aws.ToString(in.ResourceARN), "arn:aws:application-autoscaling:us-east-1:123456789012:scalable-target/")
	tags := map[string]string{"ocel:managed-by": "ocel"}
	if counts, found := a.tagged[id]; found {
		tags[minInstancesTag] = strconv.Itoa(int(counts.min))
		tags[maxInstancesTag] = strconv.Itoa(int(counts.max))
	}
	return &applicationautoscaling.ListTagsForResourceOutput{Tags: tags}, nil
}

func (a *scalingAccount) DescribeServices(_ context.Context, in *ecs.DescribeServicesInput, _ ...func(*ecs.Options)) (*ecs.DescribeServicesOutput, error) {
	if aws.ToString(in.Cluster) != fixtureCluster {
		return nil, errors.New("described services outside the tier's container cluster")
	}
	out := &ecs.DescribeServicesOutput{}
	for _, name := range in.Services {
		out.Services = append(out.Services, ecstypes.Service{
			ServiceName:   aws.String(name),
			LoadBalancers: []ecstypes.LoadBalancer{{TargetGroupArn: aws.String("arn:aws:elasticloadbalancing:us-east-1:123456789012:targetgroup/" + name)}},
		})
	}
	return out, nil
}

func (a *scalingAccount) UpdateService(_ context.Context, in *ecs.UpdateServiceInput, _ ...func(*ecs.Options)) (*ecs.UpdateServiceOutput, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if aws.ToString(in.Cluster) != fixtureCluster {
		return nil, errors.New("updated a service outside the tier's container cluster")
	}
	a.updated[aws.ToString(in.Service)] = aws.ToInt32(in.DesiredCount)
	return &ecs.UpdateServiceOutput{}, nil
}

func (a *scalingAccount) DescribeTargetHealth(_ context.Context, in *elbv2.DescribeTargetHealthInput, _ ...func(*elbv2.Options)) (*elbv2.DescribeTargetHealthOutput, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.polls++
	healthy := 0
	if a.healthy != nil {
		healthy = a.healthy(a.polls)
	}
	out := &elbv2.DescribeTargetHealthOutput{}
	for range healthy {
		out.TargetHealthDescriptions = append(out.TargetHealthDescriptions, elbv2types.TargetHealthDescription{TargetHealth: &elbv2types.TargetHealth{State: elbv2types.TargetHealthStateEnumHealthy}})
	}
	out.TargetHealthDescriptions = append(out.TargetHealthDescriptions, elbv2types.TargetHealthDescription{TargetHealth: &elbv2types.TargetHealth{State: elbv2types.TargetHealthStateEnumInitial}})
	return out, nil
}

type testClock struct {
	mu     sync.Mutex
	now    time.Time
	waited []time.Duration
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Wait(_ context.Context, d time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.waited = append(c.waited, d)
	c.now = c.now.Add(d)
	return nil
}

func scalingOver(account *scalingAccount, clock *testClock) ReleaseScaling {
	return ReleaseScaling{Namespace: scalingNamespace, AutoScaling: account, Services: account, Balancers: account, Now: clock.Now, Wait: clock.Wait}
}

func appRelease(t *testing.T, app, rendered string) provider.AppRelease {
	t.Helper()
	release, err := provider.ParseRelease(rendered)
	if err != nil {
		t.Fatal(err)
	}
	return provider.AppRelease{App: app, Release: release}
}

func serviceOf(t *testing.T, release provider.AppRelease) string {
	t.Helper()
	names, err := NamesFor(scalingNamespace, "shop", naming.AppStack("prod", release.App, release.Release.Token()))
	if err != nil {
		t.Fatal(err)
	}
	return names.Service
}

func resourceOf(t *testing.T, release provider.AppRelease) string {
	t.Helper()
	return "service/" + fixtureCluster + "/" + serviceOf(t, release)
}

var windowStart = time.Date(2026, 10, 20, 9, 30, 0, 0, time.UTC)

func TestAPromotionSchedulesThePreviousReleasesScaleToZeroAtTheWindowsEndAndScalesOlderReleasesToZeroNow(t *testing.T) {
	t.Parallel()
	previous := appRelease(t, "web", "0000000000000000000000000000000c~00000000000c")
	older := appRelease(t, "web", "0000000000000000000000000000000b~00000000000b")
	stopped := appRelease(t, "web", "0000000000000000000000000000000a~00000000000a")
	function := appRelease(t, "api", "0000000000000000000000000000000d~00000000000d")
	account := newScalingAccount()
	account.targets[resourceOf(t, previous)] = capacity{2, 4}
	account.targets[resourceOf(t, older)] = capacity{2, 4}
	account.targets[resourceOf(t, stopped)] = capacity{0, 0}
	clock := &testClock{now: windowStart}

	err := scalingOver(account, clock).ScaleDown(context.Background(), provider.DemotedReleases{
		Slug: "shop", Tier: environment.TierProduction, Env: "prod",
		Previous: []provider.AppRelease{previous, function},
		Older:    []provider.AppRelease{older, stopped},
	}, progress.Discard())
	if err != nil {
		t.Fatalf("ScaleDown() = %v", err)
	}

	scheduled := account.scheduled[resourceOf(t, previous)+"/"+scaleToZeroAction]
	if want := "at(2026-10-20T09:45:00)"; scheduled != want {
		t.Errorf("the previous release's scale to zero is scheduled %q, want %q, the end of its %s window", scheduled, want, previousReleaseWindow)
	}
	if held := account.targets[resourceOf(t, previous)]; held != (capacity{2, 4}) {
		t.Errorf("the previous release is held at %v, want it left at {2 4} until its window ends, so a rollback within it is instant", held)
	}
	if held := account.registered[resourceOf(t, older)]; held != (capacity{0, 0}) {
		t.Errorf("the older release's target was registered at %v, want it dropped to zero now", held)
	}
	if desired, set := account.updated[serviceOf(t, older)]; !set || desired != 0 {
		t.Errorf("the older release's service was set to %v (set %v), want zero tasks now", desired, set)
	}
	if _, touched := account.registered[resourceOf(t, stopped)]; touched {
		t.Error("a release already at zero was registered again, a request that changes nothing")
	}
	if len(account.scheduled) != 1 || len(account.updated) != 1 {
		t.Errorf("scheduled %v and updated %v, want the previous release scheduled and the older one updated, and nothing for a release that runs no container", account.scheduled, account.updated)
	}
}

func TestARollbackToAScaledDownReleaseScalesItBackUpAndReturnsOnlyOnceItsTasksAreHealthy(t *testing.T) {
	t.Parallel()
	rolledBackTo := appRelease(t, "web", "0000000000000000000000000000000a~00000000000a")
	account := newScalingAccount()
	account.targets[resourceOf(t, rolledBackTo)] = capacity{0, 0}
	account.tagged[resourceOf(t, rolledBackTo)] = capacity{2, 4}
	account.scheduled[resourceOf(t, rolledBackTo)+"/"+scaleToZeroAction] = "at(2026-10-20T09:00:00)"
	account.healthy = func(polls int) int { return min(polls-1, 2) }
	clock := &testClock{now: windowStart}

	err := scalingOver(account, clock).ScaleUp(context.Background(), provider.PromotedReleases{
		Slug: "shop", Tier: environment.TierProduction, Env: "prod", Releases: []provider.AppRelease{rolledBackTo},
	}, progress.Discard())
	if err != nil {
		t.Fatalf("ScaleUp() = %v", err)
	}

	if held := account.registered[resourceOf(t, rolledBackTo)]; held != (capacity{2, 4}) {
		t.Errorf("the release's target was registered at %v, want {2 4}, the instance counts it was deployed with", held)
	}
	if desired := account.updated[serviceOf(t, rolledBackTo)]; desired != 2 {
		t.Errorf("the release's service was set to %d tasks, want its minimum of 2", desired)
	}
	if account.polls != 3 {
		t.Errorf("target health was read %d times, want 3: ScaleUp returns only once both tasks are healthy behind the target group", account.polls)
	}
	if len(clock.waited) != 2 || clock.waited[1] < clock.waited[0] {
		t.Errorf("waited %v between reads, want two waits that never shrink", clock.waited)
	}
	if len(account.scheduled) != 0 {
		t.Errorf("scheduled actions %v remain, want the scale to zero cancelled so it never stops a release that serves", account.scheduled)
	}
}

func TestARollbackWithinTheWindowCancelsTheScaleDownAndWaitsOnNothing(t *testing.T) {
	t.Parallel()
	previous := appRelease(t, "web", "0000000000000000000000000000000a~00000000000a")
	account := newScalingAccount()
	account.targets[resourceOf(t, previous)] = capacity{2, 4}
	account.scheduled[resourceOf(t, previous)+"/"+scaleToZeroAction] = "at(2026-10-20T09:45:00)"
	clock := &testClock{now: windowStart}

	err := scalingOver(account, clock).ScaleUp(context.Background(), provider.PromotedReleases{
		Slug: "shop", Tier: environment.TierProduction, Env: "prod", Releases: []provider.AppRelease{previous},
	}, progress.Discard())
	if err != nil {
		t.Fatalf("ScaleUp() = %v", err)
	}

	if len(account.scheduled) != 0 {
		t.Errorf("scheduled actions %v remain, want the window's scale to zero cancelled", account.scheduled)
	}
	if len(account.registered) != 0 || len(account.updated) != 0 || account.polls != 0 || len(clock.waited) != 0 {
		t.Errorf("registered %v, updated %v, read health %d times and waited %v, want nothing: a release within its window still runs, so the pointer moves at once",
			account.registered, account.updated, account.polls, clock.waited)
	}
}

func TestARollbackToAReleaseThatNeverTurnsHealthyIsRefusedOnceTheWaitRunsOut(t *testing.T) {
	t.Parallel()
	rolledBackTo := appRelease(t, "web", "0000000000000000000000000000000a~00000000000a")
	account := newScalingAccount()
	account.targets[resourceOf(t, rolledBackTo)] = capacity{0, 0}
	account.tagged[resourceOf(t, rolledBackTo)] = capacity{1, 1}
	clock := &testClock{now: windowStart}

	err := scalingOver(account, clock).ScaleUp(context.Background(), provider.PromotedReleases{
		Slug: "shop", Tier: environment.TierProduction, Env: "prod", Releases: []provider.AppRelease{rolledBackTo},
	}, progress.Discard())

	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeNotReady {
		t.Fatalf("ScaleUp() = %v, want it refused as not ready", err)
	}
	if elapsed := clock.Now().Sub(windowStart); elapsed < scaleUpTimeout || elapsed > scaleUpTimeout+maxHealthPoll {
		t.Errorf("gave up after %s, want about %s", elapsed, scaleUpTimeout)
	}
}

func TestARollbackToAScaledDownReleaseThatNamesNoInstanceCountsIsRefused(t *testing.T) {
	t.Parallel()
	rolledBackTo := appRelease(t, "web", "0000000000000000000000000000000a~00000000000a")
	account := newScalingAccount()
	account.targets[resourceOf(t, rolledBackTo)] = capacity{0, 0}
	clock := &testClock{now: windowStart}

	err := scalingOver(account, clock).ScaleUp(context.Background(), provider.PromotedReleases{
		Slug: "shop", Tier: environment.TierProduction, Env: "prod", Releases: []provider.AppRelease{rolledBackTo},
	}, progress.Discard())

	if err == nil || !strings.Contains(err.Error(), minInstancesTag) {
		t.Fatalf("ScaleUp() = %v, want it refused naming the tag it could not read", err)
	}
	if len(account.registered) != 0 {
		t.Errorf("registered %v, want nothing scaled to counts it could not read", account.registered)
	}
}

func TestScalingReleasesThatRunNoContainerChangesNothing(t *testing.T) {
	t.Parallel()
	function := []provider.AppRelease{appRelease(t, "api", "0000000000000000000000000000000d~00000000000d")}
	account := newScalingAccount()
	clock := &testClock{now: windowStart}
	scaling := scalingOver(account, clock)

	if err := scaling.ScaleUp(context.Background(), provider.PromotedReleases{Slug: "shop", Tier: environment.TierProduction, Env: "prod", Releases: function}, progress.Discard()); err != nil {
		t.Fatalf("ScaleUp() = %v", err)
	}
	if err := scaling.ScaleDown(context.Background(), provider.DemotedReleases{Slug: "shop", Tier: environment.TierProduction, Env: "prod", Previous: function, Older: function}, progress.Discard()); err != nil {
		t.Fatalf("ScaleDown() = %v", err)
	}
	if len(account.deleted) != 0 || len(account.registered) != 0 || len(account.scheduled) != 0 || len(account.updated) != 0 {
		t.Errorf("deleted %v, registered %v, scheduled %v and updated %v, want nothing touched for a release with no scalable target",
			account.deleted, account.registered, account.scheduled, account.updated)
	}
}

func TestScalingReadsTheTargetsOfManyReleasesInBatchesTheAPIAccepts(t *testing.T) {
	t.Parallel()
	var older []provider.AppRelease
	for i := range 120 {
		older = append(older, appRelease(t, "web", strings.Repeat("0", 29)+strconv.FormatInt(int64(1000+i), 16)+"~"+strings.Repeat("0", 12)))
	}
	account := newScalingAccount()
	clock := &testClock{now: windowStart}

	if err := scalingOver(account, clock).ScaleDown(context.Background(), provider.DemotedReleases{Slug: "shop", Tier: environment.TierProduction, Env: "prod", Older: older}, progress.Discard()); err != nil {
		t.Fatalf("ScaleDown() = %v", err)
	}
	if account.described != 3 {
		t.Errorf("described targets %d times for 120 releases, want 3 batches of at most 50", account.described)
	}
}

func TestARefusedScaleUpScalesTheReleaseItStartedBackToZeroSoARetryWaitsForItsHealthAgain(t *testing.T) {
	t.Parallel()
	rolledBackTo := appRelease(t, "web", "0000000000000000000000000000000a~00000000000a")
	account := newScalingAccount()
	account.targets[resourceOf(t, rolledBackTo)] = capacity{0, 0}
	account.tagged[resourceOf(t, rolledBackTo)] = capacity{1, 1}
	clock := &testClock{now: windowStart}
	scaling := scalingOver(account, clock)
	promoted := provider.PromotedReleases{Slug: "shop", Tier: environment.TierProduction, Env: "prod", Releases: []provider.AppRelease{rolledBackTo}}

	if err := scaling.ScaleUp(context.Background(), promoted, progress.Discard()); err == nil {
		t.Fatal("ScaleUp() = nil, want a release that never turns healthy refused")
	}
	if held := account.targets[resourceOf(t, rolledBackTo)]; held != (capacity{0, 0}) {
		t.Errorf("after the refusal the release's target is held at %v, want {0 0}, as it was before the promotion started it", held)
	}
	if desired := account.updated[serviceOf(t, rolledBackTo)]; desired != 0 {
		t.Errorf("after the refusal the release's service is set to %d tasks, want 0", desired)
	}

	polls := account.polls
	if err := scaling.ScaleUp(context.Background(), promoted, progress.Discard()); err == nil {
		t.Errorf("a retried ScaleUp() = nil after %d more health reads, want it refused again rather than moving the pointer to a release whose tasks are not healthy", account.polls-polls)
	}
}

func TestARefusedScaleUpSchedulesTheScaleToZeroItCancelledAgain(t *testing.T) {
	t.Parallel()
	previous := appRelease(t, "web", "0000000000000000000000000000000c~00000000000c")
	unhealthy := appRelease(t, "api", "0000000000000000000000000000000a~00000000000a")
	account := newScalingAccount()
	account.targets[resourceOf(t, previous)] = capacity{2, 4}
	account.scheduled[resourceOf(t, previous)+"/"+scaleToZeroAction] = "at(2026-10-20T09:45:00)"
	account.targets[resourceOf(t, unhealthy)] = capacity{0, 0}
	account.tagged[resourceOf(t, unhealthy)] = capacity{1, 1}
	clock := &testClock{now: windowStart}

	err := scalingOver(account, clock).ScaleUp(context.Background(), provider.PromotedReleases{
		Slug: "shop", Tier: environment.TierProduction, Env: "prod", Releases: []provider.AppRelease{previous, unhealthy},
	}, progress.Discard())
	if err == nil {
		t.Fatal("ScaleUp() = nil, want a release that never turns healthy refused")
	}

	want := "at(" + clock.Now().UTC().Add(previousReleaseWindow).Format(scheduleLayout) + ")"
	if scheduled := account.scheduled[resourceOf(t, previous)+"/"+scaleToZeroAction]; scheduled != want {
		t.Errorf("after the refusal the previous release's scale to zero is scheduled %q, want %q, so a release the refused promotion never served does not keep running until another promotion", scheduled, want)
	}
	if held := account.targets[resourceOf(t, previous)]; held != (capacity{2, 4}) {
		t.Errorf("after the refusal the previous release is held at %v, want {2 4} until its window ends", held)
	}
}
