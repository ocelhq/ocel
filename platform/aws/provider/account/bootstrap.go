package account

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	cfntypes "github.com/aws/aws-sdk-go-v2/service/cloudformation/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
	"golang.org/x/sync/errgroup"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/aws/provider/cfn"
	"github.com/ocelhq/ocel/platform/aws/provider/tagclock"
)

const (
	StateTableIndexName = tagclock.IndexName

	outputStateTable          = "StateTableName"
	outputStateTableARN       = "StateTableArn"
	outputStateTableStreamARN = "StateTableStreamArn"
	outputInfraTier           = "InfrastructureTier"
)

type Deployed struct {
	Present           bool
	Stacks            []StackStamp
	Parts             PartSet
	Buckets           naming.Buckets
	StateTable        string
	VariablesTable    string
	VariablesTableARN string
	VariablesKeyARN   string
	AppBoundaryARN    string

	StateTableStreamARN   string
	TagInvalidatorRoleARN string
	EdgeInvokeRoleARN     string

	Tier    environment.Tier
	Outputs map[string]string
}

type SSMAPI interface {
	SSMBatchAPI
	GetParameter(ctx context.Context, in *ssm.GetParameterInput, optFns ...func(*ssm.Options)) (*ssm.GetParameterOutput, error)
	PutParameter(ctx context.Context, in *ssm.PutParameterInput, optFns ...func(*ssm.Options)) (*ssm.PutParameterOutput, error)
	DeleteParameter(ctx context.Context, in *ssm.DeleteParameterInput, optFns ...func(*ssm.Options)) (*ssm.DeleteParameterOutput, error)
}

type APIs struct {
	CFN     cfn.API
	SSM     SSMAPI
	IAM     IAMAPI
	Layers  LayerAPI
	Store   ObjectStore
	Records RecordStore
	Edge    edge.Edge
	Edges   provider.Edges
}

type Request struct {
	VariablesKey       string
	Parts              []string
	Remove             []string
	Writer             provider.WrittenBy
	AcceptReplacements bool
}

func CheckDeployed(ctx context.Context, api cfn.StacksAPI, ns Namespace) (Deployed, error) {
	return CheckDeployedFor(ctx, api, ns, environment.TierProduction)
}

func CheckDeployedPreview(ctx context.Context, api cfn.StacksAPI, ns Namespace) (Deployed, error) {
	return CheckDeployedFor(ctx, api, ns, environment.TierPreview)
}

func CheckDeployedFor(ctx context.Context, api cfn.StacksAPI, ns Namespace, tier environment.Tier) (Deployed, error) {
	deployed, _, err := readBootstrap(ctx, api, ns, tier)
	return deployed, err
}

type Reading struct {
	Deployed Deployed
	ns       Namespace
	tier     environment.Tier
	refs     stackRefs
}

func (r Reading) Tier() environment.Tier { return r.tier }

func (r Reading) Namespace() Namespace { return r.ns }

func Read(ctx context.Context, api cfn.StacksAPI, ns Namespace, tier environment.Tier) (Reading, error) {
	deployed, refs, err := readBootstrap(ctx, api, ns, tier)
	if err != nil {
		return Reading{}, err
	}
	return Reading{Deployed: deployed, ns: ns, tier: tier, refs: refs}, nil
}

func readCore(ctx context.Context, api cfn.StacksAPI, ns Namespace, tier environment.Tier) (Deployed, stackRefs, *cfntypes.Stack, error) {
	coreStack, err := ns.StackNameFor(tier)
	if err != nil {
		return Deployed{}, stackRefs{}, nil, err
	}
	core, err := cfn.DescribeStack(ctx, api, coreStack)
	if err != nil {
		return Deployed{}, stackRefs{}, nil, err
	}
	if core == nil || cfn.Unusable(core.StackStatus) {
		return Deployed{Present: false, Parts: PartSet{}}, stackRefs{bucketTag: naming.NewRandomTag()}, nil, nil
	}
	d := Deployed{Present: true, Parts: PartSet{}, Buckets: naming.Buckets{}, Outputs: cfn.OutputsOf(core)}
	var refs stackRefs
	absorb(&d, &refs, d.Outputs)
	if refs.bucketTag, err = ns.bootstrapBucketTag(tier, d.Buckets); err != nil {
		return Deployed{}, stackRefs{}, nil, err
	}
	return d, refs, core, nil
}

func readBootstrap(ctx context.Context, api cfn.StacksAPI, ns Namespace, tier environment.Tier) (Deployed, stackRefs, error) {
	d, refs, core, err := readCore(ctx, api, ns, tier)
	if err != nil || !d.Present {
		return d, refs, err
	}
	coreStamp := readStamp(core.Tags)

	stamps := make(map[string]Stamp, len(partRegistry))
	for _, f := range partRegistry {
		stack, err := cfn.DescribeStack(ctx, api, f.stackName(ns, tier))
		if err != nil {
			return Deployed{}, stackRefs{}, err
		}
		if stack == nil || cfn.Unusable(stack.StackStatus) {
			continue
		}
		d.Parts[f.name] = true
		stamps[f.name] = readStamp(stack.Tags)
		out := cfn.OutputsOf(stack)
		maps.Copy(d.Outputs, out)
		absorb(&d, &refs, out)
	}

	target, err := bootstrapFor(ns, tier)
	if err != nil {
		return Deployed{}, stackRefs{}, err
	}
	intendedCore, err := target.core(broughtVariablesKey(d.Outputs), refs.bucketTag)
	if err != nil {
		return Deployed{}, stackRefs{}, err
	}
	d.Stacks = append(d.Stacks, StackStamp{
		Name:      target.stackName,
		Present:   true,
		Digest:    coreStamp.Digest,
		Intended:  cfn.TemplateDigest(intendedCore.body),
		WrittenBy: coreStamp.WrittenBy,
	})
	for _, f := range partRegistry {
		if !d.Parts.Has(f.name) {
			d.Stacks = append(d.Stacks, StackStamp{Name: f.stackName(ns, tier), Part: f.name})
			continue
		}
		stamp := stamps[f.name]
		intended, err := f.template(partInputs{
			ns:           ns,
			tier:         tier,
			refs:         refs,
			alongside:    d.Parts,
			variablesKey: broughtVariablesKey(d.Outputs),
		})
		if err != nil {
			return Deployed{}, stackRefs{}, err
		}
		d.Stacks = append(d.Stacks, StackStamp{
			Name:      f.stackName(ns, tier),
			Part:      f.name,
			Present:   true,
			Digest:    stamp.Digest,
			Intended:  cfn.TemplateDigest(intended.body),
			WrittenBy: stamp.WrittenBy,
		})
	}
	return d, refs, nil
}

func broughtVariablesKey(outputs map[string]string) string {
	if outputs[outputVariablesKeyBrought] != "true" {
		return ""
	}
	return outputs[outputVariablesKeyARN]
}

func installedParts(ctx context.Context, api cfn.StacksAPI, ns Namespace, tier environment.Tier) (PartSet, error) {
	installed := PartSet{}
	for _, f := range partRegistry {
		stack, err := cfn.DescribeStack(ctx, api, f.stackName(ns, tier))
		if err != nil {
			return nil, err
		}
		if stack != nil {
			installed[f.name] = true
		}
	}
	return installed, nil
}

func absorb(d *Deployed, refs *stackRefs, out map[string]string) {
	for key, value := range out {
		if class, ok := bucketOutputs[key]; ok {
			d.Buckets[class] = value
			continue
		}
		switch key {
		case outputStateTable:
			d.StateTable, refs.stateTable = value, value
		case outputStateTableARN:
			refs.stateTableARN = value
		case outputStateTableStreamARN:
			d.StateTableStreamARN, refs.stateTableStreamARN = value, value
		case outputTagInvalidatorRoleARN:
			d.TagInvalidatorRoleARN = value
		case OutputEdgeInvokeRoleARN:
			d.EdgeInvokeRoleARN = value
		case outputEdgeBucketARN:
			refs.edgeBucketARN = value
		case outputVariablesTable:
			d.VariablesTable = value
		case outputVariablesTableARN:
			d.VariablesTableARN = value
		case outputVariablesKeyARN:
			d.VariablesKeyARN = value
		case outputAppBoundaryARN:
			d.AppBoundaryARN = value
		case outputInfraTier:
			d.Tier = environment.Tier(value)
		}
	}
}

type spec struct {
	ns        Namespace
	tier      environment.Tier
	stackName string
	stackStep string
}

func (s spec) core(broughtKey, bucketTag string) (partStack, error) {
	return newPartStack(coreStackTemplate(s.ns, s.tier, broughtKey, bucketTag), nil)
}

func productionBootstrap(ns Namespace) spec {
	name, _ := ns.StackNameFor(environment.TierProduction)
	return spec{
		ns:        ns,
		tier:      environment.TierProduction,
		stackName: name,
		stackStep: "Ensuring Pulumi state bucket and state table (CloudFormation)",
	}
}

func previewBootstrap(ns Namespace) spec {
	name, _ := ns.StackNameFor(environment.TierPreview)
	return spec{
		ns:        ns,
		tier:      environment.TierPreview,
		stackName: name,
		stackStep: "Ensuring preview infrastructure (CloudFormation)",
	}
}

func bootstrapFor(ns Namespace, tier environment.Tier) (spec, error) {
	switch tier {
	case environment.TierProduction:
		return productionBootstrap(ns), nil
	case environment.TierPreview:
		return previewBootstrap(ns), nil
	default:
		return spec{}, fmt.Errorf("bootstrap: unknown tier %q", tier)
	}
}

func Run(ctx context.Context, apis APIs, ns Namespace, tier environment.Tier, req Request, progress progress.Log) error {
	target, err := specFor(ns, tier)
	if err != nil {
		return err
	}
	return run(ctx, apis, target, req, ensureProgress(progress))
}

func ensureProgress(runProgress progress.Log) progress.Log {
	if runProgress == nil {
		return progress.Discard()
	}
	return runProgress
}

func specFor(ns Namespace, tier environment.Tier) (spec, error) {
	switch tier {
	case environment.TierProduction:
		return productionBootstrap(ns), nil
	case environment.TierPreview:
		return previewBootstrap(ns), nil
	default:
		return spec{}, fmt.Errorf("there is no %s bootstrap; a bootstrap is either production or preview", tier)
	}
}

func run(ctx context.Context, apis APIs, target spec, req Request, progress progress.Log) error {
	requested := req.Parts
	for _, name := range requested {
		if _, known := partNamed(name); !known {
			return refusal.Refuse(refusal.CodeInvalid, "this provider has no bootstrap part named %q", name)
		}
	}

	progress.Say("Ensuring the secret the origin's own front authenticates with (SSM SecureString)")
	secret, err := ensureOriginSecret(ctx, apis.SSM, target.ns, target.tier, time.Now())
	if err != nil {
		return err
	}
	if secret.retired {
		progress.Warn(fmt.Sprintf("Retired the %s origin secret a rotation replaced: a release not re-deployed since no longer answers its front", target.tier))
	}
	switch {
	case secret.rotated:
		progress.Warn(fmt.Sprintf("Rotated the %s origin secret, which was older than %d days: re-deploy each project in the tier so its releases accept the new one; the first bootstrap after %d days retires the old one", target.tier, int(OriginSecretMaxAge.Hours()/24), int(OriginSecretGrace.Hours()/24)))
	case secret.minted:
		progress.Say(fmt.Sprintf("Minted a new %s origin secret", target.tier))
	default:
		progress.Debug(fmt.Sprintf("Reused the existing %s origin secret", target.tier))
	}

	alongside := PartSet{}
	for _, name := range requested {
		alongside[name] = true
	}

	progress.Say(target.stackStep)
	namedIAM := []cfntypes.Capability{cfntypes.CapabilityCapabilityNamedIam}
	review := AdmitReplacements(target.ns, req.AcceptReplacements, progress)
	_, current, _, err := readCore(ctx, apis.CFN, target.ns, target.tier)
	if err != nil {
		return err
	}
	core, err := target.core(coreVariablesKey(alongside, req.VariablesKey), current.bucketTag)
	if err != nil {
		return err
	}
	coreTags := stampTags(target.ns, target.tier, Stamp{Digest: cfn.TemplateDigest(core.body), WrittenBy: req.Writer.String()})
	if err := cfn.Upsert(ctx, apis.CFN, target.ns.ChangeSetNameFor, target.stackName, core.body, nil, namedIAM, coreTags, review); err != nil {
		return err
	}
	deployed, refs, _, err := readCore(ctx, apis.CFN, target.ns, target.tier)
	if err != nil {
		return err
	}
	progress.Say(runtimeLayerStep)
	if _, err := EnsureRuntimeLayers(ctx, apis, target.ns, target.tier, RuntimeLayerRequest{ArtifactBucket: deployed.Buckets[naming.StorageArtifacts], StateTable: deployed.StateTable}, progress); err != nil {
		return err
	}
	steps := stepDeps{ns: target.ns, tier: target.tier, ssm: apis.SSM, iam: apis.IAM, progress: progress}
	dropped := Removing(req.Parts, req.Remove)
	if err := tearDownEdges(ctx, apis, steps, dropped); err != nil {
		return err
	}
	if err := bootstrapEdges(ctx, apis, steps, req.Parts, dropped); err != nil {
		return err
	}

	progress.Say("Ensuring the Pulumi passphrase (SSM SecureString)")
	created, err := ensurePassphrase(ctx, apis.SSM, target.ns, target.tier)
	if err != nil {
		return err
	}
	if created {
		progress.Say("Generated a new Pulumi passphrase")
	} else {
		progress.Debug("Reused the existing Pulumi passphrase")
	}

	if err := dropParts(ctx, apis.CFN, steps, req.Remove); err != nil {
		return err
	}

	if len(requested) > 0 {
		progress.Say(fmt.Sprintf("Applying %s (CloudFormation)", strings.Join(partStackNames(target.ns, requested, target.tier), ", ")))
		group, gctx := errgroup.WithContext(ctx)
		for _, name := range requested {
			group.Go(func() error {
				f, _ := partNamed(name)
				stackName := f.stackName(target.ns, target.tier)
				stack, err := f.template(partInputs{
					ns:           target.ns,
					tier:         target.tier,
					refs:         refs,
					alongside:    alongside,
					variablesKey: req.VariablesKey,
				})
				if err != nil {
					return fmt.Errorf("%s: %w", name, err)
				}
				tags := stampTags(target.ns, target.tier, Stamp{Digest: cfn.TemplateDigest(stack.body), WrittenBy: req.Writer.String()})
				if err := cfn.Upsert(gctx, apis.CFN, target.ns.ChangeSetNameFor, stackName, stack.body, stack.params, namedIAM, tags, review); err != nil {
					return fmt.Errorf("%s: %w", name, err)
				}
				progress.Say("Applied stack " + stackName)
				return nil
			})
		}
		if err := group.Wait(); err != nil {
			return err
		}

		for _, name := range requested {
			f, _ := partNamed(name)
			if f.after == nil {
				continue
			}
			if err := f.after(ctx, steps); err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
		}
	}

	return nil
}

func dropParts(ctx context.Context, stacks cfn.API, steps stepDeps, dropOrder []string) error {
	if len(dropOrder) == 0 {
		return nil
	}
	steps.progress.Say(fmt.Sprintf("Removing %s (CloudFormation)", strings.Join(partStackNames(steps.ns, dropOrder, steps.tier), ", ")))
	for _, name := range dropOrder {
		f, _ := partNamed(name)
		if f.drop == nil {
			continue
		}
		if err := f.drop(ctx, steps); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	return deletePartStacks(ctx, stacks, steps.ns, steps.tier, dropOrder, steps.progress)
}

func openEdge(apis APIs, kind edge.Kind) (edge.Edge, error) {
	if apis.Edge != nil && apis.Edge.Kind() == kind {
		return apis.Edge, nil
	}
	if apis.Edges == nil {
		return nil, fmt.Errorf("bootstrap: this run has no edge registry, so it cannot reach the %s edge its parts depend on", kind)
	}
	return apis.Edges.Open(kind, nil)
}

func bootstrapEdges(ctx context.Context, apis APIs, d stepDeps, parts, dropped []string) error {
	torn := EdgeKindsFor(dropped)
	if !slices.Contains(torn, apis.Edge.Kind()) {
		if err := bootstrapEdge(ctx, d, apis.Edge); err != nil {
			return err
		}
	}
	for _, kind := range EdgeKindsFor(parts) {
		if kind == apis.Edge.Kind() || slices.Contains(torn, kind) {
			continue
		}
		front, err := openEdge(apis, kind)
		if err != nil {
			return err
		}
		if err := bootstrapEdge(ctx, d, front); err != nil {
			return err
		}
	}
	return nil
}

func tearDownEdges(ctx context.Context, apis APIs, d stepDeps, dropped []string) error {
	for _, kind := range EdgeKindsFor(dropped) {
		front, err := openEdge(apis, kind)
		if err != nil {
			return err
		}
		if err := tearDownEdge(ctx, d, front); err != nil {
			return err
		}
	}
	return nil
}

func tearDownEdge(ctx context.Context, d stepDeps, front edge.Edge) error {
	d.progress.Say(fmt.Sprintf("Tearing the %s edge down", front.Kind()))
	if err := front.Teardown(ctx, d.tier); err != nil {
		return fmt.Errorf("tear down %s edge: %w", front.Kind(), err)
	}
	return nil
}

func bootstrapEdge(ctx context.Context, d stepDeps, front edge.Edge) error {
	d.progress.Say(fmt.Sprintf("Bootstrapping the %s edge", front.Kind()))
	out, err := front.Bootstrap(ctx, d.tier)
	if err != nil {
		return fmt.Errorf("bootstrap %s edge: %w", front.Kind(), err)
	}
	if out.ReleasesStore == nil {
		return nil
	}
	d.progress.Say(fmt.Sprintf("Adopting the %s edge's releases-store worker (SSM SecureString)", front.Kind()))
	if err := adoptReleasesStore(ctx, d.ssm, d.ns, d.tier, front.Kind(), *out.ReleasesStore); err != nil {
		return err
	}
	_, err = ensurePurgeSeed(ctx, d.ssm, d.ns, d.tier, front.Kind())
	return err
}

func ensurePassphrase(ctx context.Context, ssmClient SSMAPI, ns Namespace, tier environment.Tier) (created bool, err error) {
	_, err = ssmClient.GetParameter(ctx, &ssm.GetParameterInput{
		Name:           aws.String(ns.PassphraseParamFor(tier)),
		WithDecryption: aws.Bool(true),
	})
	if err == nil {
		return false, nil
	}
	var notFound *ssmtypes.ParameterNotFound
	if !errors.As(err, &notFound) {
		return false, fmt.Errorf("read passphrase parameter: %w", err)
	}

	passphrase, err := generatePassphrase()
	if err != nil {
		return false, err
	}
	if _, err := ssmClient.PutParameter(ctx, &ssm.PutParameterInput{
		Name:        aws.String(ns.PassphraseParamFor(tier)),
		Description: aws.String("Ocel: the passphrase every " + string(tier) + " Pulumi stack in this account is encrypted under. This is the only copy - delete it and that state can never be decrypted again."),
		Value:       aws.String(passphrase),
		Type:        ssmtypes.ParameterTypeSecureString,
		Overwrite:   aws.Bool(false),
	}); err != nil {
		return false, fmt.Errorf("write passphrase parameter: %w", err)
	}
	return true, nil
}

func ReadPassphrase(ctx context.Context, ssmClient SSMAPI, ns Namespace, tier environment.Tier) (string, error) {
	out, err := ssmClient.GetParameter(ctx, &ssm.GetParameterInput{
		Name:           aws.String(ns.PassphraseParamFor(tier)),
		WithDecryption: aws.Bool(true),
	})
	if err != nil {
		return "", fmt.Errorf("read passphrase parameter: %w", err)
	}
	return aws.ToString(out.Parameter.Value), nil
}

func generatePassphrase() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate passphrase: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func coreVariablesKey(alongside PartSet, brought string) string {
	if alongside.Has(provider.VariablesKeyPart) {
		return brought
	}
	return ""
}

func coreStackTemplate(ns Namespace, tier environment.Tier, broughtKey, bucketTag string) cfn.Template {
	buckets := ns.bootstrapBuckets(tier, bucketTag)
	return cfn.Template{
		Description: coreStackDescription(tier),
		Resources: []cfn.Resource{
			stateBucketResource(tier, buckets[naming.StorageState]),
			stateTableResource(),
			artifactBucketResource(buckets[naming.StorageArtifacts]),
			edgeBucketResource(buckets[naming.StorageEdge]),
			edgeBucketPolicyResource(),
			cacheBucketResource(buckets[naming.StorageCache]),
			variablesTableResource(tier),
			appBoundaryResource(ns, tier, broughtKey),
			tagInvalidatorRoleResource(ns, tier),
			edgeInvokeRoleResource(ns, tier),
		},
		Outputs: slices.Concat(
			[]cfn.Output{stateBucketOutput(tier)},
			stateTableOutputs(),
			[]cfn.Output{artifactBucketOutput()},
			edgeBucketOutputs(),
			[]cfn.Output{cacheBucketOutput()},
			variablesOutputs(),
			[]cfn.Output{appBoundaryOutput()},
			baseRoleOutputs(),
			[]cfn.Output{{
				Name:        outputInfraTier,
				Description: "The tier this bootstrap was installed for: production or preview.",
				Value:       string(tier),
			}},
		),
	}
}

func coreStackDescription(tier environment.Tier) string {
	apart := ""
	if tier == environment.TierPreview {
		apart = " It is kept apart from the production bootstrap so a per-PR preview never reaches production state, variables or caches."
	}
	return "Ocel bootstrap (" + string(tier) + ") - the account-global core every " + scopeOf(tier) + " Ocel deploys here is built on: the Pulumi state bucket and state table, the artifact, edge and cache buckets, the variable store and the roles the tag invalidator and API Gateway run as. Each other piece a deploy needs is added beside it on first use." + apart
}

func scopeOf(tier environment.Tier) string {
	if tier == environment.TierPreview {
		return "preview environment"
	}
	return "production app"
}

func keyedTableAttributes() []any {
	attributes := make([]any, 0, 4)
	for _, name := range []string{"pk", "sk", "gsi1pk", "gsi1sk"} {
		attributes = append(attributes, map[string]any{"AttributeName": name, "AttributeType": "S"})
	}
	return attributes
}

func keySchema(hash, rangeKey string) []any {
	return []any{
		map[string]any{"AttributeName": hash, "KeyType": "HASH"},
		map[string]any{"AttributeName": rangeKey, "KeyType": "RANGE"},
	}
}

func stateTableResource() cfn.Resource {
	return cfn.Resource{
		ID:   "StateTable",
		Type: "AWS::DynamoDB::Table",
		Metadata: cfn.Metadata{
			Description: "Account-global Ocel state, keyed by pk/sk: the index of every stack this bootstrap has deployed, which prune and teardown walk, and the tag clock the edge reads and updates.",
		},
		Properties: map[string]any{
			"BillingMode":          "PAY_PER_REQUEST",
			"AttributeDefinitions": keyedTableAttributes(),
			"KeySchema":            keySchema("pk", "sk"),
			"GlobalSecondaryIndexes": []any{
				map[string]any{
					"IndexName": StateTableIndexName,
					"KeySchema": keySchema("gsi1pk", "gsi1sk"),
					"Projection": map[string]any{
						"ProjectionType":   "INCLUDE",
						"NonKeyAttributes": []string{"expired", "stale", "tag"},
					},
				},
			},
			"TimeToLiveSpecification": map[string]any{"AttributeName": "expires_at", "Enabled": true},
			"StreamSpecification":     map[string]any{"StreamViewType": "NEW_IMAGE"},
		},
	}
}

func stateTableOutputs() []cfn.Output {
	return []cfn.Output{
		{
			Name:        outputStateTable,
			Description: "DynamoDB table storing account-global Ocel state: the stack index prune and teardown walk, and the ISR tag clock the edge shares.",
			Value:       cfn.Ref("StateTable"),
		},
		{
			Name:        outputStateTableARN,
			Description: "ARN of that table, handed to every part stack that grants an item read or write on it.",
			Value:       cfn.GetAtt("StateTable", "Arn"),
		},
		{
			Name:        outputStateTableStreamARN,
			Description: "ARN of that table's stream, handed to every part stack that reads tag writes off it.",
			Value:       cfn.GetAtt("StateTable", "StreamArn"),
		},
	}
}
