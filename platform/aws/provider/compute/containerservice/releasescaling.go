package containerservice

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/applicationautoscaling"
	scalingtypes "github.com/aws/aws-sdk-go-v2/service/applicationautoscaling/types"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	elbv2 "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbv2types "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
)

const (
	previousReleaseWindow = 15 * time.Minute

	scaleToZeroAction     = "ocel-scale-to-zero"
	scheduleLayout        = "2006-01-02T15:04:05"
	targetsPerRead        = 50
	servicesPerRead       = 10
	scaleUpTimeout        = 5 * time.Minute
	lowerTimeout          = 30 * time.Second
	firstHealthPoll       = 5 * time.Second
	maxHealthPoll         = 20 * time.Second
	healthPollGrowthRatio = 2
)

type AutoScalingAPI interface {
	DescribeScalableTargets(ctx context.Context, in *applicationautoscaling.DescribeScalableTargetsInput, opts ...func(*applicationautoscaling.Options)) (*applicationautoscaling.DescribeScalableTargetsOutput, error)
	RegisterScalableTarget(ctx context.Context, in *applicationautoscaling.RegisterScalableTargetInput, opts ...func(*applicationautoscaling.Options)) (*applicationautoscaling.RegisterScalableTargetOutput, error)
	PutScheduledAction(ctx context.Context, in *applicationautoscaling.PutScheduledActionInput, opts ...func(*applicationautoscaling.Options)) (*applicationautoscaling.PutScheduledActionOutput, error)
	DeleteScheduledAction(ctx context.Context, in *applicationautoscaling.DeleteScheduledActionInput, opts ...func(*applicationautoscaling.Options)) (*applicationautoscaling.DeleteScheduledActionOutput, error)
	ListTagsForResource(ctx context.Context, in *applicationautoscaling.ListTagsForResourceInput, opts ...func(*applicationautoscaling.Options)) (*applicationautoscaling.ListTagsForResourceOutput, error)
}

type ServicesAPI interface {
	DescribeServices(ctx context.Context, in *ecs.DescribeServicesInput, opts ...func(*ecs.Options)) (*ecs.DescribeServicesOutput, error)
	UpdateService(ctx context.Context, in *ecs.UpdateServiceInput, opts ...func(*ecs.Options)) (*ecs.UpdateServiceOutput, error)
}

type TargetHealthAPI interface {
	DescribeTargetHealth(ctx context.Context, in *elbv2.DescribeTargetHealthInput, opts ...func(*elbv2.Options)) (*elbv2.DescribeTargetHealthOutput, error)
}

type ReleaseScaling struct {
	Namespace   string
	AutoScaling AutoScalingAPI
	Services    ServicesAPI
	Balancers   TargetHealthAPI
	Now         func() time.Time
	Wait        func(ctx context.Context, d time.Duration) error
}

type releaseService struct {
	release  provider.AppRelease
	cluster  string
	name     string
	resource string
}

type startedRelease struct {
	releaseService
	tasks int32
}

func (s ReleaseScaling) newReleaseServices(tier environment.Tier, slug, env string, releases []provider.AppRelease) ([]releaseService, error) {
	cluster := awsports.ContainerClusterName(s.Namespace, tier)
	services := make([]releaseService, 0, len(releases))
	for _, release := range releases {
		stack := naming.AppStack(env, release.App, release.Release.Token())
		name, err := NameAt(s.Namespace, Coordinate(naming.Sanitize(slug), stack))
		if err != nil {
			return nil, err
		}
		services = append(services, releaseService{release: release, cluster: cluster, name: name, resource: "service/" + cluster + "/" + name})
	}
	return services, nil
}

func (s ReleaseScaling) ScaleUp(ctx context.Context, promoted provider.PromotedReleases, progress progress.Log) error {
	services, err := s.newReleaseServices(promoted.Tier, promoted.Slug, promoted.Env, promoted.Releases)
	if err != nil {
		return err
	}
	targets, err := s.readTargets(ctx, services)
	if err != nil {
		return err
	}
	if raised, err := s.raise(ctx, services, targets, progress); err != nil {
		return errors.Join(err, s.lower(ctx, raised))
	}
	return nil
}

type raisedReleases struct {
	cancelled []releaseService
	started   []startedRelease
}

func (s ReleaseScaling) raise(ctx context.Context, services []releaseService, targets map[string]scalingtypes.ScalableTarget, progress progress.Log) (raisedReleases, error) {
	var raised raisedReleases
	var scaled []releaseService
	for _, service := range services {
		if _, found := targets[service.resource]; !found {
			continue
		}
		cancelled, err := s.cancelScaleToZero(ctx, service)
		if err != nil {
			return raised, err
		}
		if cancelled {
			raised.cancelled = append(raised.cancelled, service)
		}
		scaled = append(scaled, service)
	}
	targets, err := s.readTargets(ctx, scaled)
	if err != nil {
		return raised, err
	}
	for _, service := range scaled {
		if runsTasks(targets, service) {
			continue
		}
		least, most, err := s.readInstanceCounts(ctx, service, targets[service.resource])
		if err != nil {
			return raised, err
		}
		raised.started = append(raised.started, startedRelease{releaseService: service, tasks: least})
		if err := s.scale(ctx, service, least, most); err != nil {
			return raised, err
		}
		progress.Say(fmt.Sprintf("Scaling %s release %s back up to %d tasks before it serves", service.release.App, service.release.Release, least))
	}
	return raised, s.waitHealthy(ctx, raised.started)
}

func (s ReleaseScaling) lower(ctx context.Context, raised raisedReleases) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), lowerTimeout)
	defer cancel()
	var errs []error
	stopped := map[string]bool{}
	for _, release := range raised.started {
		stopped[release.resource] = true
		if err := s.scale(ctx, release.releaseService, 0, 0); err != nil {
			errs = append(errs, err)
		}
	}
	at := s.Now().UTC().Add(previousReleaseWindow)
	for _, service := range raised.cancelled {
		if stopped[service.resource] {
			continue
		}
		if err := s.scheduleScaleToZero(ctx, service, at); err != nil {
			errs = append(errs, err)
		}
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("the releases this refused promotion scaled up keep running until a later promotion scales them down: %w", err)
	}
	return nil
}

func (s ReleaseScaling) ScaleDown(ctx context.Context, demoted provider.DemotedReleases, progress progress.Log) error {
	previous, err := s.newReleaseServices(demoted.Tier, demoted.Slug, demoted.Env, demoted.Previous)
	if err != nil {
		return err
	}
	older, err := s.newReleaseServices(demoted.Tier, demoted.Slug, demoted.Env, demoted.Older)
	if err != nil {
		return err
	}
	targets, err := s.readTargets(ctx, append(slices.Clone(previous), older...))
	if err != nil {
		return err
	}
	at := s.Now().UTC().Add(previousReleaseWindow)
	var errs []error
	for _, service := range previous {
		if !runsTasks(targets, service) {
			continue
		}
		if err := s.scheduleScaleToZero(ctx, service, at); err != nil {
			errs = append(errs, err)
			continue
		}
		progress.Say(fmt.Sprintf("%s release %s keeps running until %s, so a rollback to it is instant, then scales to zero tasks", service.release.App, service.release.Release, at.Format("15:04 MST")))
	}
	for _, service := range older {
		if !runsTasks(targets, service) {
			continue
		}
		if err := s.scale(ctx, service, 0, 0); err != nil {
			errs = append(errs, err)
			continue
		}
		progress.Say(fmt.Sprintf("Scaled %s release %s to zero tasks", service.release.App, service.release.Release))
	}
	return errors.Join(errs...)
}

func runsTasks(targets map[string]scalingtypes.ScalableTarget, service releaseService) bool {
	target, found := targets[service.resource]
	return found && aws.ToInt32(target.MaxCapacity) > 0
}

func (s ReleaseScaling) readTargets(ctx context.Context, services []releaseService) (map[string]scalingtypes.ScalableTarget, error) {
	targets := map[string]scalingtypes.ScalableTarget{}
	resources := make([]string, 0, len(services))
	for _, service := range services {
		resources = append(resources, service.resource)
	}
	slices.Sort(resources)
	for batch := range slices.Chunk(slices.Compact(resources), targetsPerRead) {
		in := &applicationautoscaling.DescribeScalableTargetsInput{
			ServiceNamespace:  scalingtypes.ServiceNamespaceEcs,
			ScalableDimension: scalingtypes.ScalableDimensionECSServiceDesiredCount,
			ResourceIds:       batch,
		}
		for {
			read, err := s.AutoScaling.DescribeScalableTargets(ctx, in)
			if err != nil {
				return nil, fmt.Errorf("read the scalable targets of %v: %w", batch, err)
			}
			for _, target := range read.ScalableTargets {
				targets[aws.ToString(target.ResourceId)] = target
			}
			if aws.ToString(read.NextToken) == "" {
				break
			}
			in.NextToken = read.NextToken
		}
	}
	return targets, nil
}

func (s ReleaseScaling) cancelScaleToZero(ctx context.Context, service releaseService) (bool, error) {
	_, err := s.AutoScaling.DeleteScheduledAction(ctx, &applicationautoscaling.DeleteScheduledActionInput{
		ServiceNamespace:    scalingtypes.ServiceNamespaceEcs,
		ScalableDimension:   scalingtypes.ScalableDimensionECSServiceDesiredCount,
		ResourceId:          aws.String(service.resource),
		ScheduledActionName: aws.String(scaleToZeroAction),
	})
	var absent *scalingtypes.ObjectNotFoundException
	if errors.As(err, &absent) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("cancel the scale to zero of %s release %s: %w", service.release.App, service.release.Release, err)
	}
	return true, nil
}

func (s ReleaseScaling) scheduleScaleToZero(ctx context.Context, service releaseService, at time.Time) error {
	_, err := s.AutoScaling.PutScheduledAction(ctx, &applicationautoscaling.PutScheduledActionInput{
		ServiceNamespace:     scalingtypes.ServiceNamespaceEcs,
		ScalableDimension:    scalingtypes.ScalableDimensionECSServiceDesiredCount,
		ResourceId:           aws.String(service.resource),
		ScheduledActionName:  aws.String(scaleToZeroAction),
		Schedule:             aws.String("at(" + at.Format(scheduleLayout) + ")"),
		Timezone:             aws.String("UTC"),
		ScalableTargetAction: &scalingtypes.ScalableTargetAction{MinCapacity: aws.Int32(0), MaxCapacity: aws.Int32(0)},
	})
	if err != nil {
		return fmt.Errorf("schedule %s release %s to scale to zero at %s: %w", service.release.App, service.release.Release, at.Format(time.RFC3339), err)
	}
	return nil
}

func (s ReleaseScaling) readInstanceCounts(ctx context.Context, service releaseService, target scalingtypes.ScalableTarget) (int32, int32, error) {
	tagged, err := s.AutoScaling.ListTagsForResource(ctx, &applicationautoscaling.ListTagsForResourceInput{ResourceARN: target.ScalableTargetARN})
	if err != nil {
		return 0, 0, fmt.Errorf("read the instance counts of %s release %s: %w", service.release.App, service.release.Release, err)
	}
	least, leastErr := readInstanceCount(tagged.Tags, MinInstancesTag)
	most, mostErr := readInstanceCount(tagged.Tags, MaxInstancesTag)
	if err := errors.Join(leastErr, mostErr); err != nil || least < 1 || most < least {
		return 0, 0, refusal.Refuse(refusal.CodeNotReady,
			"%s release %s runs no tasks, and its scalable target names no instance counts to scale it back up to in %s and %s: deploy its build again rather than roll back to it",
			service.release.App, service.release.Release, MinInstancesTag, MaxInstancesTag)
	}
	return least, most, nil
}

func readInstanceCount(tags map[string]string, key string) (int32, error) {
	count, err := strconv.ParseInt(tags[key], 10, 32)
	return int32(count), err
}

func (s ReleaseScaling) scale(ctx context.Context, service releaseService, least, most int32) error {
	if _, err := s.AutoScaling.RegisterScalableTarget(ctx, &applicationautoscaling.RegisterScalableTargetInput{
		ServiceNamespace:  scalingtypes.ServiceNamespaceEcs,
		ScalableDimension: scalingtypes.ScalableDimensionECSServiceDesiredCount,
		ResourceId:        aws.String(service.resource),
		MinCapacity:       aws.Int32(least),
		MaxCapacity:       aws.Int32(most),
	}); err != nil {
		return fmt.Errorf("hold %s release %s between %d and %d tasks: %w", service.release.App, service.release.Release, least, most, err)
	}
	if _, err := s.Services.UpdateService(ctx, &ecs.UpdateServiceInput{
		Cluster:      aws.String(service.cluster),
		Service:      aws.String(service.name),
		DesiredCount: aws.Int32(least),
	}); err != nil {
		return fmt.Errorf("set %s release %s to %d tasks: %w", service.release.App, service.release.Release, least, err)
	}
	return nil
}

func (s ReleaseScaling) waitHealthy(ctx context.Context, started []startedRelease) error {
	if len(started) == 0 {
		return nil
	}
	groups, err := s.readTargetGroups(ctx, started)
	if err != nil {
		return err
	}
	deadline := s.Now().Add(scaleUpTimeout)
	pending := slices.Clone(started)
	for delay := firstHealthPoll; ; delay = min(delay*healthPollGrowthRatio, maxHealthPoll) {
		var unhealthy []startedRelease
		for _, release := range pending {
			healthy, err := s.countHealthy(ctx, groups[release.name])
			if err != nil {
				return fmt.Errorf("read the health of %s release %s: %w", release.release.App, release.release.Release, err)
			}
			if healthy < release.tasks {
				unhealthy = append(unhealthy, release)
			}
		}
		if pending = unhealthy; len(pending) == 0 {
			return nil
		}
		if !s.Now().Before(deadline) {
			first := pending[0]
			return refusal.Refuse(refusal.CodeNotReady,
				"%s release %s was scaled back up to %d tasks, and they were not all healthy behind its target group within %s, so nothing was switched to it: read its logs, then roll back again or deploy",
				first.release.App, first.release.Release, first.tasks, scaleUpTimeout)
		}
		if err := s.Wait(ctx, delay); err != nil {
			return err
		}
	}
}

func (s ReleaseScaling) readTargetGroups(ctx context.Context, started []startedRelease) (map[string]string, error) {
	byName := map[string]startedRelease{}
	for _, release := range started {
		byName[release.name] = release
	}
	groups := map[string]string{}
	for batch := range slices.Chunk(slices.Sorted(maps.Keys(byName)), servicesPerRead) {
		read, err := s.Services.DescribeServices(ctx, &ecs.DescribeServicesInput{Cluster: aws.String(started[0].cluster), Services: batch})
		if err != nil {
			return nil, fmt.Errorf("read the services %v: %w", batch, err)
		}
		for _, service := range read.Services {
			for _, balanced := range service.LoadBalancers {
				if group := aws.ToString(balanced.TargetGroupArn); group != "" {
					groups[aws.ToString(service.ServiceName)] = group
				}
			}
		}
	}
	for name, release := range byName {
		if groups[name] == "" {
			return nil, fmt.Errorf("the cluster runs no service %s behind a target group, which %s release %s runs as", name, release.release.App, release.release.Release)
		}
	}
	return groups, nil
}

func (s ReleaseScaling) countHealthy(ctx context.Context, group string) (int32, error) {
	read, err := s.Balancers.DescribeTargetHealth(ctx, &elbv2.DescribeTargetHealthInput{TargetGroupArn: aws.String(group)})
	if err != nil {
		return 0, err
	}
	var healthy int32
	for _, target := range read.TargetHealthDescriptions {
		if target.TargetHealth != nil && target.TargetHealth.State == elbv2types.TargetHealthStateEnumHealthy {
			healthy++
		}
	}
	return healthy, nil
}
