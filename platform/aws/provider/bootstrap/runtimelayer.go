package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	smithy "github.com/aws/smithy-go"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/aws/provider/payloads"
)

const (
	runtimeLayerKeyPrefix = "ocel-runtime-layer"
	runtimeLayerLabel     = "function runtime"
	runtimeDigestLen      = 12

	runtimeStackStep = "Ensuring the runtime every function boots through (CloudFormation)"
)

var runtimeArchTokens = map[string]string{
	providerkit.ArchX8664: "X8664",
	providerkit.ArchARM64: "Arm64",
}

func runtimeArches() []string { return []string{providerkit.ArchX8664, providerkit.ArchARM64} }

func runtimeLayerResourceID(arch string) string { return "RuntimeLayer" + runtimeArchTokens[arch] }

func shortRuntimeDigest(digest string) string {
	if len(digest) <= runtimeDigestLen {
		return digest
	}
	return digest[:runtimeDigestLen]
}

func runtimeLayerOutputKey(arch, digest string) string {
	return runtimeLayerResourceID(arch) + shortRuntimeDigest(digest) + "Arn"
}

func runtimeLayerName(ns Namespace, class, arch, digest string) string {
	return suffixed(class, string(ns)+"-runtime") + "-" + runtimeArchTokens[arch] + "-" + shortRuntimeDigest(digest)
}

func runtimeLayerArches() map[string]string {
	arches := make(map[string]string, len(runtimeArches()))
	for _, arch := range runtimeArches() {
		carried, err := payloads.RuntimeLayer(arch)
		if err != nil {
			continue
		}
		arches[runtimeLayerOutputKey(arch, carried.SHA256)] = arch
	}
	return arches
}

func carriedRuntimeArches() []string {
	arches := slices.Collect(maps.Values(runtimeLayerArches()))
	slices.Sort(arches)
	return arches
}

func runtimeLayerPlacements(bucket string) (map[string]payloads.Placement, error) {
	placed := make(map[string]payloads.Placement, len(runtimeArches()))
	for _, arch := range runtimeArches() {
		carried, err := payloads.RuntimeLayer(arch)
		if err != nil {
			return nil, err
		}
		placed[arch] = payloads.At(bucket, runtimeLayerKeyPrefix, carried)
	}
	return placed, nil
}

func placeRuntimeLayers(ctx context.Context, store ObjectStore, bucket string) (map[string]payloads.Placement, error) {
	placed := make(map[string]payloads.Placement, len(runtimeArches()))
	for _, arch := range runtimeArches() {
		carried, err := payloads.RuntimeLayer(arch)
		if err != nil {
			return nil, err
		}
		at, err := payloads.Place(ctx, store, bucket, runtimeLayerKeyPrefix, runtimeLayerLabel+" ("+arch+")", carried)
		if err != nil {
			return nil, err
		}
		placed[arch] = at
	}
	return placed, nil
}

func runtimeLayerTemplateAt(ns Namespace, class, bucket string) (string, error) {
	placed, err := runtimeLayerPlacements(bucket)
	if err != nil {
		return "", err
	}
	return runtimeLayerTemplate(ns, class, placed), nil
}

func runtimeLayerTemplate(ns Namespace, class string, code map[string]payloads.Placement) string {
	var resources, outputs strings.Builder
	for _, arch := range runtimeArches() {
		at := code[arch]
		id := runtimeLayerResourceID(arch)
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
`, id, runtimeLayerName(ns, class, arch, at.SHA256),
			fmt.Sprintf("Ocel %s runtime (%s) - payload %s", arch, class, shortRuntimeDigest(at.SHA256)),
			at.Bucket, at.Key, arch)
		fmt.Fprintf(&outputs, `  %s:
    Description: "Version ARN of the %s runtime, named after the payload it carries so a release only ever boots through the runtime the build that deploys it carries."
    Value: !Ref %s
`, runtimeLayerOutputKey(arch, at.SHA256), arch, id)
	}
	return fmt.Sprintf(`AWSTemplateFormatVersion: '2010-09-09'
Description: %q
Resources:
%sOutputs:
%s`, runtimeStackDescription(class), resources.String(), outputs.String())
}

func runtimeStackDescription(class string) string {
	return fmt.Sprintf("Ocel bootstrap runtime (%s) - one Lambda layer per architecture holding the runtime every function deployed from this bootstrap boots through, published once per account rather than once per release.", class)
}

func applyRuntimeLayers(ctx context.Context, apis APIs, target spec, req Request, bucket string, progressf, logf func(string)) error {
	progressf(runtimeStackStep)
	code, err := placeRuntimeLayers(ctx, apis.Store, bucket)
	if err != nil {
		return err
	}
	stackName := target.ns.runtimeStackName(target.class)
	body := runtimeLayerTemplate(target.ns, target.class, code)
	tags := stampTags(target.ns, Stamp{Schema: RequiredSchema, Digest: TemplateDigest(body), WrittenBy: req.Writer.String()})
	if err := upsertCFNStack(ctx, apis.CFN, target.ns, stackName, body, nil, nil, tags, nil); err != nil {
		return err
	}
	logf(fmt.Sprintf("applied %s", stackName))
	return nil
}

type RuntimeLayerRequest struct {
	ArtifactBucket string
	Writer         providerkit.Writer
}

func EnsureRuntimeLayers(ctx context.Context, apis APIs, ns Namespace, class string, req RuntimeLayerRequest, log func(string)) (map[string]string, error) {
	if log == nil {
		log = func(string) {}
	}
	stackName := ns.runtimeStackName(class)
	held, err := publishedRuntimeLayers(ctx, apis.CFN, ns, class)
	if err != nil || len(missingRuntimeLayers(held)) == 0 {
		return held, err
	}
	target, err := specFor(ns, class)
	if err != nil {
		return nil, err
	}

	silent := func(string) {}
	published := true
	if err := applyRuntimeLayers(ctx, apis, target, Request{Writer: req.Writer}, req.ArtifactBucket, silent, silent); err != nil {
		if !runtimeStackHeldElsewhere(err) {
			log(fmt.Sprintf("could not publish this build's runtime into %s: %v", stackName, err))
			return held, nil
		}
		published = false
		if _, err := settleStack(ctx, apis.CFN, stackName, log); err != nil {
			return nil, err
		}
	}

	standing, err := publishedRuntimeLayers(ctx, apis.CFN, ns, class)
	if err != nil {
		return nil, err
	}
	if len(missingRuntimeLayers(standing)) > 0 {
		return standing, nil
	}
	who := "another deploy"
	if published {
		who = "this deploy"
	}
	log(fmt.Sprintf("%s published this build's runtime for %s into %s", who, strings.Join(carriedRuntimeArches(), ", "), stackName))
	return standing, nil
}

func publishedRuntimeLayers(ctx context.Context, api CFNDescriber, ns Namespace, class string) (map[string]string, error) {
	out, err := stackOutputs(ctx, api, ns.runtimeStackName(class))
	if err != nil {
		return nil, err
	}
	standing := Deployed{RuntimeLayers: map[string]string{}}
	var refs stackRefs
	if err := absorb(&standing, &refs, out); err != nil {
		return nil, err
	}
	return standing.RuntimeLayers, nil
}

func missingRuntimeLayers(held map[string]string) []string {
	var missing []string
	for _, arch := range carriedRuntimeArches() {
		if held[arch] == "" {
			missing = append(missing, arch)
		}
	}
	return missing
}

func runtimeStackHeldElsewhere(err error) bool {
	var api smithy.APIError
	if errors.As(err, &api) && api.ErrorCode() == "AlreadyExistsException" {
		return true
	}
	return isValidationErrorContaining(err, "state and can not be updated")
}

func planRuntimeLayers(ctx context.Context, cfn CFNAPI, read Reading, req Request) providerkit.ChangeGroup {
	stamp := read.Deployed.RuntimeStack
	group := providerkit.ChangeGroup{Kind: providerkit.StackGroupKind, Name: read.ns.runtimeStackName(read.class)}
	body, err := runtimeLayerTemplateAt(read.ns, read.class, read.Deployed.ArtifactBucket)
	if err != nil {
		group.Action, group.Reason = providerkit.ActionKeep, err.Error()
		return group
	}
	switch {
	case !stamp.Present:
		group.Action = providerkit.ActionCreate
		group.Changes = templateChanges(body, providerkit.ActionCreate)
	case stamp.Current():
		group.Action, group.Reason = providerkit.ActionKeep, "already current"
	default:
		group.Action = providerkit.ActionUpdate
		group = planUpdate(ctx, cfn, read.ns, group, featureStack{body: body}, req.Writer)
	}
	return group
}

func removeRuntimeLayers(ctx context.Context, cfn CFNAPI, read Reading) providerkit.ChangeGroup {
	group := providerkit.ChangeGroup{
		Kind:   providerkit.StackGroupKind,
		Name:   read.ns.runtimeStackName(read.class),
		Action: providerkit.ActionDelete,
	}
	body, err := runtimeLayerTemplateAt(read.ns, read.class, read.Deployed.ArtifactBucket)
	if err != nil {
		return group
	}
	return planDelete(ctx, cfn, group, body)
}

func deleteRuntimeLayerStack(ctx context.Context, cfn CFNTeardownAPI, ns Namespace, class string, log func(string)) error {
	stackName := ns.runtimeStackName(class)
	stack, err := describeStack(ctx, cfn, stackName)
	if err != nil || stack == nil {
		return err
	}
	if err := deleteCFNStack(ctx, cfn, stackName); err != nil {
		return err
	}
	if log != nil {
		log(fmt.Sprintf("removed %s", stackName))
	}
	return nil
}
