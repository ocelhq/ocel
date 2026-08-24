package deploy

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	ec2 "github.com/pulumi/pulumi-aws/sdk/v7/go/aws/ec2"
	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	sdk "github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/naming"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	linksv1 "github.com/ocelhq/ocel/pkg/proto/common/links/v1"
	"github.com/ocelhq/ocel/pkg/providerkit"
	kitpulumi "github.com/ocelhq/ocel/pkg/providerkit/pulumi"
	"github.com/ocelhq/ocel/platform/aws/provider/payloads"
)

type Releaser struct {
	cfg      Config
	stacks   StackIndex
	realized *Realized
	adapter  *kitpulumi.Adapter

	served *servedApps
}

func NewReleaser(cfg Config, realized *Realized) *Releaser {
	return newReleaser(cfg, realized, nil)
}

func newReleaser(cfg Config, realized *Realized, engine kitpulumi.Engine) *Releaser {
	r := &Releaser{cfg: cfg, stacks: cfg.Stacks, realized: realized, served: newServedApps()}
	r.adapter = kitpulumi.New(kitpulumi.Config{
		Access: kitpulumi.Access{
			BackendURL: cfg.BackendURL,
			Passphrase: cfg.Passphrase,
			Project:    cfg.PulumiProject,
			Env:        map[string]string{"AWS_REGION": cfg.Region},
		},
		Program: r,
		Refresh: refreshPolicy(realized),
		Engine:  engine,
	})
	return r
}

func Serves() []providerkit.LinkType {
	return []providerkit.LinkType{providerkit.LinkPostgres, providerkit.LinkBucket}
}

func EnsurePulumi(ctx context.Context, say func(string)) error {
	return kitpulumi.Install(ctx, reporterFor(nil, StageID{}, say, nil))
}

func releaserFor(access PulumiAccess, stacks StackIndex, realized *Realized, engine kitpulumi.Engine) *Releaser {
	return newReleaser(Config{
		Region:        access.Region,
		BackendURL:    access.BackendURL,
		Passphrase:    access.Passphrase,
		PulumiProject: access.PulumiProject,
		Stacks:        stacks,
	}, realized, engine)
}

func refreshPolicy(realized *Realized) func(providerkit.StackRef, kitpulumi.Op) bool {
	return func(ref providerkit.StackRef, op kitpulumi.Op) bool {
		if op != kitpulumi.OpDestroy || skipTeardownRefresh() {
			return false
		}
		return !realized.realizedHere(naming.Sanitize(ref.Project), ref.Name)
	}
}

type stackWork struct {
	program sdk.RunFunc
	tags    map[string]string
	outputs auto.OutputMap
}

func (r *Releaser) Run(pctx *sdk.Context, plan providerkit.StackPlan) error {
	switch work := plan.Options.(type) {
	case *stackWork:
		return work.program(pctx)
	case *appWork:
		return work.run(pctx)
	}
	if plan.Kind != providerkit.StackInfra {
		return providerkit.Refuse(providerkit.CodeInvalid,
			"%s stands up an app and this plan carries none", plan.Ref.Name)
	}
	return r.infra(pctx, plan)
}

func (r *Releaser) infra(pctx *sdk.Context, plan providerkit.StackPlan) error {
	vpc, err := ec2.LookupVpc(pctx, &ec2.LookupVpcArgs{Default: sdk.BoolRef(true)})
	if err != nil {
		return fmt.Errorf("look up default VPC: %w", err)
	}
	subnets, err := ec2.GetSubnets(pctx, &ec2.GetSubnetsArgs{
		Filters: []ec2.GetSubnetsFilter{{Name: "vpc-id", Values: []string{vpc.Id}}},
	})
	if err != nil {
		return fmt.Errorf("look up default VPC subnets: %w", err)
	}

	project, env := naming.Sanitize(plan.Ref.Project), plan.Ref.Name.Env
	transformed := transformsOf(plan)
	sessions := newSessionScope(project, env, r.cfg.StateTableARN)

	for _, resource := range plan.Resources {
		if resource.Linked {
			continue
		}
		var err error
		switch resource.Type {
		case providerkit.LinkPostgres:
			args := transformed.forPostgres(resource.Name, postgresConfig(resource))
			err = registerPostgres(pctx, project, env, resource.Name, args, vpc.Id, vpc.CidrBlock, subnets.Ids)
		case providerkit.LinkBucket:
			args := transformed.forBucket(resource.Name, bucketConfig(resource))
			err = registerBucket(pctx, project, env, resource.Name, args, r.cfg.StateTable, r.cfg.AppBoundaryARN, sessions, payloads.Placement{})
		default:
			return providerkit.Refuse(providerkit.CodeInvalid,
				"this provider stands up no %s; it stands up %s and %s", resource.Type, providerkit.LinkPostgres, providerkit.LinkBucket)
		}
		if err != nil {
			return fmt.Errorf("declare %s: %w", resource.Name, err)
		}
	}
	return nil
}

func transformsOf(plan providerkit.StackPlan) *transformedArgs {
	switch held := plan.Options.(type) {
	case *transformedArgs:
		return held
	case *appWork:
		return held.transformed
	}
	return nil
}

func postgresConfig(resource providerkit.Resource) *resourcesv1.PostgresConfig {
	if resource.Postgres == nil {
		return nil
	}
	return &resourcesv1.PostgresConfig{Version: resource.Postgres.Version}
}

func bucketConfig(resource providerkit.Resource) *resourcesv1.BucketConfig {
	if resource.Bucket == nil {
		return nil
	}
	return &resourcesv1.BucketConfig{AllowedOrigins: resource.Bucket.AllowedOrigins}
}

func (r *Releaser) Configure(_ context.Context, plan providerkit.StackPlan) (auto.ConfigMap, error) {
	tags := plan.Tags
	if work, held := plan.Options.(*stackWork); held {
		tags = work.tags
	}
	if len(tags) == 0 {
		return auto.ConfigMap{}, nil
	}
	encoded, err := json.Marshal(map[string]map[string]string{"tags": tags})
	if err != nil {
		return nil, fmt.Errorf("render default tags: %w", err)
	}
	return auto.ConfigMap{"aws:defaultTags": auto.ConfigValue{Value: string(encoded)}}, nil
}

func (r *Releaser) Decode(ctx context.Context, plan providerkit.StackPlan, outputs auto.OutputMap) (providerkit.StackResult, error) {
	if work, held := plan.Options.(*stackWork); held {
		work.outputs = outputs
		return providerkit.StackResult{}, nil
	}
	if plan.App != nil {
		return r.decodeApp(plan, outputs)
	}
	sessions := newSessionScope(naming.Sanitize(plan.Ref.Project), plan.Ref.Name.Env, r.cfg.StateTableARN)
	result := providerkit.StackResult{}
	for _, resource := range plan.Resources {
		if resource.Linked {
			continue
		}
		raw, produced := outputs[resource.Name]
		if !produced {
			return providerkit.StackResult{}, fmt.Errorf("stack produced no output for %s", resource.Name)
		}
		fields, mapped := raw.Value.(map[string]any)
		if !mapped {
			return providerkit.StackResult{}, fmt.Errorf("output for %s is not a map", resource.Name)
		}
		var (
			link *linksv1.Link
			err  error
		)
		switch resource.Type {
		case providerkit.LinkPostgres:
			link, err = collectPostgresLink(ctx, r.cfg.Secrets, resource.Name, fields)
		case providerkit.LinkBucket:
			link, err = collectBucketLink(resource.Name, sessions, fields)
		}
		if err != nil {
			return providerkit.StackResult{}, err
		}
		held := linkOf(resource.Type, link)
		held.Resource = resource.Declared
		result.Links = append(result.Links, held)
	}
	return result, nil
}

func linkOf(kind providerkit.LinkType, link *linksv1.Link) providerkit.Link {
	properties := map[string]string{}
	switch kind {
	case providerkit.LinkPostgres:
		p := link.GetPostgres()
		properties[providerkit.PropertyHost] = p.GetHost()
		properties[providerkit.PropertyPort] = strconv.Itoa(int(p.GetPort()))
		properties[providerkit.PropertyDatabase] = p.GetDatabase()
		properties[providerkit.PropertyUsername] = p.GetUsername()
		properties[providerkit.PropertyPassword] = p.GetPassword()
	case providerkit.LinkBucket:
		properties[providerkit.PropertyBucket] = link.GetBucket().GetBucket()
	}
	return providerkit.Link{
		Type:       kind,
		Name:       link.GetName(),
		Properties: properties,
		Grants:     providerkit.GrantsOf(link),
	}
}

func (r *Releaser) refuseHandover(ctx context.Context, plan providerkit.StackPlan) error {
	var linked []providerkit.Resource
	for _, resource := range plan.Resources {
		if resource.Linked {
			linked = append(linked, resource)
		}
	}
	if len(linked) == 0 {
		return nil
	}
	outputs, err := r.adapter.Outputs(ctx, plan.Ref, nil)
	if err != nil {
		return err
	}
	var handed []string
	for _, resource := range linked {
		if _, provisioned := outputs[resource.Name]; provisioned {
			handed = append(handed, resource.Declared)
		}
	}
	if len(handed) == 0 {
		return nil
	}
	return &HandoverError{Links: handed, Stack: plan.Ref.Name.String()}
}

func (r *Releaser) Provision(ctx context.Context, plan providerkit.StackPlan, report providerkit.Reporter) (providerkit.StackResult, error) {
	if err := r.index(ctx, plan.Ref); err != nil {
		return providerkit.StackResult{}, err
	}
	if plan.Options == nil {
		transformed, err := transformStackPlan(ctx, r.cfg.Transform, plan)
		if err != nil {
			return providerkit.StackResult{}, err
		}
		if plan.App == nil {
			if err := r.refuseHandover(ctx, plan); err != nil {
				return providerkit.StackResult{}, err
			}
			plan.Options = transformed
		} else {
			work, err := r.appWork(plan, transformed)
			if err != nil {
				return providerkit.StackResult{}, err
			}
			plan.Options = work
		}
	}
	return r.adapter.Run(ctx, plan, report)
}

type TagSweeper interface {
	SweepTagClock(ctx context.Context, project string, stack naming.StackName) error
}

func (r *Releaser) Destroy(ctx context.Context, ref providerkit.StackRef, report providerkit.Reporter) error {
	index, err := stackIndex(r.stacks)
	if err != nil {
		return err
	}
	if err := r.adapter.Destroy(ctx, ref, report); err != nil {
		return err
	}
	project := naming.Sanitize(ref.Project)
	if sweeper, sweeps := index.(TagSweeper); sweeps {
		if err := sweeper.SweepTagClock(ctx, project, ref.Name); err != nil {
			return err
		}
	}
	return index.RemoveStack(ctx, project, ref.Name)
}

func (r *Releaser) index(ctx context.Context, ref providerkit.StackRef) error {
	index, err := stackIndex(r.stacks)
	if err != nil {
		return err
	}
	return r.realized.realize(ctx, index, naming.Sanitize(ref.Project), ref.Name)
}

func (r *Releaser) Inspect(ctx context.Context, ref providerkit.StackRef) (providerkit.StackState, error) {
	outputs, err := r.adapter.Outputs(ctx, ref, nil)
	if err != nil {
		return providerkit.StackState{}, err
	}
	return providerkit.StackState{Present: len(outputs) > 0}, nil
}

func (r *Releaser) Outputs(ctx context.Context, ref providerkit.StackRef, report providerkit.Reporter) (auto.OutputMap, error) {
	return r.adapter.Outputs(ctx, ref, report)
}

func (r *Releaser) up(
	ctx context.Context,
	ref providerkit.StackRef,
	tags map[string]string,
	program sdk.RunFunc,
	report providerkit.Reporter,
) (auto.OutputMap, error) {
	work := &stackWork{program: program, tags: tags}
	if _, err := r.Provision(ctx, providerkit.StackPlan{Ref: ref, Options: work}, report); err != nil {
		return nil, err
	}
	return work.outputs, nil
}

var _ providerkit.Releaser = (*Releaser)(nil)
