package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	smithy "github.com/aws/smithy-go"

	"github.com/ocelhq/ocel/pkg/arch"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/cfn"
	"github.com/ocelhq/ocel/platform/aws/provider/payloads"
)

const (
	runtimeLayerKeyPrefix = "ocel-runtime-layer"
	runtimeLayerLabel     = "function runtime"
	runtimeDigestLen      = 12

	runtimeStackStep = "Ensuring the runtime every function boots through (CloudFormation)"
)

var runtimeArchTokens = map[string]string{
	arch.X8664: "X8664",
	arch.ARM64: "Arm64",
}

func runtimeArches() []string { return []string{arch.X8664, arch.ARM64} }

func runtimeLayerResourceID(architecture string) string {
	return "RuntimeLayer" + runtimeArchTokens[architecture]
}

func shortRuntimeDigest(digest string) string {
	if len(digest) <= runtimeDigestLen {
		return digest
	}
	return digest[:runtimeDigestLen]
}

func runtimeLayerOutputKey(architecture, digest string) string {
	return runtimeLayerResourceID(architecture) + shortRuntimeDigest(digest) + "Arn"
}

func runtimeLayerName(ns Namespace, tier environment.Tier, architecture, digest string) string {
	return suffixed(tier, string(ns)+"-runtime") + "-" + runtimeArchTokens[architecture] + "-" + shortRuntimeDigest(digest)
}

func runtimeLayerArches() map[string]string {
	arches := make(map[string]string, len(runtimeArches()))
	for _, architecture := range runtimeArches() {
		shipped, err := payloads.RuntimeLayer(architecture)
		if err != nil {
			continue
		}
		arches[runtimeLayerOutputKey(architecture, shipped.SHA256)] = architecture
	}
	return arches
}

func shippedRuntimeArches() []string {
	arches := slices.Collect(maps.Values(runtimeLayerArches()))
	slices.Sort(arches)
	return arches
}

func runtimeLayerPlacements(bucket string) (map[string]payloads.Placement, error) {
	placed := make(map[string]payloads.Placement, len(runtimeArches()))
	for _, architecture := range runtimeArches() {
		shipped, err := payloads.RuntimeLayer(architecture)
		if err != nil {
			return nil, err
		}
		placed[architecture] = payloads.At(bucket, runtimeLayerKeyPrefix, shipped)
	}
	return placed, nil
}

func placeRuntimeLayers(ctx context.Context, store ObjectStore, bucket string) (map[string]payloads.Placement, error) {
	placed := make(map[string]payloads.Placement, len(runtimeArches()))
	for _, architecture := range runtimeArches() {
		shipped, err := payloads.RuntimeLayer(architecture)
		if err != nil {
			return nil, err
		}
		at, err := payloads.Place(ctx, store, bucket, runtimeLayerKeyPrefix, runtimeLayerLabel+" ("+architecture+")", shipped)
		if err != nil {
			return nil, err
		}
		placed[architecture] = at
	}
	return placed, nil
}

func runtimeLayerTemplateAt(ns Namespace, tier environment.Tier, bucket string) (string, error) {
	placed, err := runtimeLayerPlacements(bucket)
	if err != nil {
		return "", err
	}
	return runtimeLayerTemplate(ns, tier, placed), nil
}

func runtimeLayerTemplate(ns Namespace, tier environment.Tier, code map[string]payloads.Placement) string {
	var resources, outputs strings.Builder
	for _, architecture := range runtimeArches() {
		at := code[architecture]
		id := runtimeLayerResourceID(architecture)
		fmt.Fprintf(&resources, `  %s:
    Type: AWS::Lambda::LayerVersion
    Properties:
      LayerName: %s
      Description: %q
      Content:
        S3Bucket: %s
        S3Key: %s
      CompatibleArchitectures:
        - %s
`, id, runtimeLayerName(ns, tier, architecture, at.SHA256),
			fmt.Sprintf("Ocel %s runtime (%s) - payload %s", architecture, tier, shortRuntimeDigest(at.SHA256)),
			at.Bucket, at.Key, architecture)
		fmt.Fprintf(&outputs, `  %s:
    Description: "Version ARN of the %s runtime, named after the payload it contains so a release only ever boots through the runtime the build that deploys it ships."
    Value: !Ref %s
`, runtimeLayerOutputKey(architecture, at.SHA256), architecture, id)
	}
	return fmt.Sprintf(`AWSTemplateFormatVersion: '2010-09-09'
Description: %q
Resources:
%sOutputs:
%s`, runtimeStackDescription(tier), resources.String(), outputs.String())
}

func runtimeStackDescription(tier environment.Tier) string {
	return fmt.Sprintf("Ocel bootstrap runtime (%s) - one Lambda layer per architecture containing the runtime every function deployed from this bootstrap boots through, published once per account rather than once per release.", tier)
}

func applyRuntimeLayers(ctx context.Context, apis APIs, target spec, req Request, bucket string, progress progress.Progress) error {
	progress.Say(runtimeStackStep)
	code, err := placeRuntimeLayers(ctx, apis.Store, bucket)
	if err != nil {
		return err
	}
	stackName := target.ns.runtimeStackName(target.tier)
	body := runtimeLayerTemplate(target.ns, target.tier, code)
	tags := stampTags(target.ns, Stamp{Schema: provider.BootstrapSchema, Digest: cfn.TemplateDigest(body), WrittenBy: req.Writer.String()})
	if err := cfn.Upsert(ctx, apis.CFN, target.ns.ChangeSetNameFor, stackName, body, nil, nil, tags, nil); err != nil {
		return err
	}
	progress.Say("Applied stack " + stackName)
	return nil
}

type RuntimeLayerRequest struct {
	ArtifactBucket string
	Writer         provider.WrittenBy
}

func EnsureRuntimeLayers(ctx context.Context, apis APIs, ns Namespace, tier environment.Tier, req RuntimeLayerRequest, runProgress progress.Progress) (map[string]string, error) {
	runProgress = ensureProgress(runProgress)
	stackName := ns.runtimeStackName(tier)
	current, err := publishedRuntimeLayers(ctx, apis.CFN, ns, tier)
	if err != nil || len(missingRuntimeLayers(current)) == 0 {
		return current, err
	}
	target, err := specFor(ns, tier)
	if err != nil {
		return nil, err
	}

	published := true
	if err := applyRuntimeLayers(ctx, apis, target, Request{Writer: req.Writer}, req.ArtifactBucket, progress.DiscardProgress()); err != nil {
		if !runtimeStackWrittenElsewhere(err) {
			runProgress.Warn(fmt.Sprintf("Could not publish this build's runtime into stack %s: %v", stackName, err))
			return current, nil
		}
		published = false
		if _, err := awaitStackIdle(ctx, apis.CFN, stackName, runProgress); err != nil {
			return nil, err
		}
	}

	latest, err := publishedRuntimeLayers(ctx, apis.CFN, ns, tier)
	if err != nil {
		return nil, err
	}
	if len(missingRuntimeLayers(latest)) > 0 {
		return latest, nil
	}
	who := "Another deploy"
	if published {
		who = "This deploy"
	}
	runProgress.Say(fmt.Sprintf("%s published this build's runtime for %s into stack %s", who, strings.Join(shippedRuntimeArches(), ", "), stackName))
	return latest, nil
}

func publishedRuntimeLayers(ctx context.Context, api cfn.StacksAPI, ns Namespace, tier environment.Tier) (map[string]string, error) {
	out, err := cfn.StackOutputs(ctx, api, ns.runtimeStackName(tier))
	if err != nil {
		return nil, err
	}
	deployed := Deployed{RuntimeLayers: map[string]string{}}
	var refs stackRefs
	if err := absorb(&deployed, &refs, out); err != nil {
		return nil, err
	}
	return deployed.RuntimeLayers, nil
}

func missingRuntimeLayers(layers map[string]string) []string {
	var missing []string
	for _, architecture := range shippedRuntimeArches() {
		if layers[architecture] == "" {
			missing = append(missing, architecture)
		}
	}
	return missing
}

func runtimeStackWrittenElsewhere(err error) bool {
	var api smithy.APIError
	if errors.As(err, &api) && api.ErrorCode() == "AlreadyExistsException" {
		return true
	}
	return cfn.IsValidationErrorContaining(err, "state and can not be updated")
}

func planRuntimeLayers(ctx context.Context, stacks cfn.API, read Reading, req Request) provider.ChangeGroup {
	stamp := read.Deployed.RuntimeStack
	group := provider.ChangeGroup{Kind: provider.StackGroupKind, Name: read.ns.runtimeStackName(read.tier)}
	body, err := runtimeLayerTemplateAt(read.ns, read.tier, read.Deployed.ArtifactBucket)
	if err != nil {
		group.Action, group.Reason = provider.ActionKeep, err.Error()
		return group
	}
	switch {
	case !stamp.Present:
		group.Action = provider.ActionCreate
		group.Changes = templateChanges(body, provider.ActionCreate)
	case stamp.Current():
		group.Action, group.Reason = provider.ActionKeep, "already current"
	default:
		group.Action = provider.ActionUpdate
		group = planUpdate(ctx, stacks, read.ns, group, featureStack{body: body}, req.Writer)
	}
	return group
}

func removeRuntimeLayers(ctx context.Context, stacks cfn.API, read Reading) provider.ChangeGroup {
	group := provider.ChangeGroup{
		Kind:   provider.StackGroupKind,
		Name:   read.ns.runtimeStackName(read.tier),
		Action: provider.ActionDelete,
	}
	body, err := runtimeLayerTemplateAt(read.ns, read.tier, read.Deployed.ArtifactBucket)
	if err != nil {
		return group
	}
	return planDelete(ctx, stacks, group, body)
}

func deleteRuntimeLayerStack(ctx context.Context, stacks cfn.TeardownAPI, ns Namespace, tier environment.Tier, progress progress.Progress) error {
	stackName := ns.runtimeStackName(tier)
	stack, err := cfn.DescribeStack(ctx, stacks, stackName)
	if err != nil || stack == nil {
		return err
	}
	if err := cfn.Delete(ctx, stacks, stackName); err != nil {
		return err
	}
	progress.Say("Removed stack " + stackName)
	return nil
}
