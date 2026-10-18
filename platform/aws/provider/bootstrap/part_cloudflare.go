package bootstrap

import (
	"context"
	"fmt"
	"time"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/cfn"
	"github.com/ocelhq/ocel/platform/aws/provider/iam"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/imageoptimizer"
)

const KindCloudflare = "cloudflare"

var cloudflareEdgePart = part{
	name:      CloudflareEdgePart,
	summary:   "Cloudflare as the front — workers and the credential it reads the asset bucket with",
	edges:     []edge.Kind{KindCloudflare},
	template:  cloudflareEdgeTemplate,
	after:     mintEdgeCredentials,
	afterPlan: plannedEdgeCredentials,
	drop:      severCloudflareEdge,
	dropPlan:  plannedCloudflareSever,
}

func cloudflareEdgeTemplate(in partInputs) (partStack, error) {
	params, values := crossStack([]crossStackParam{
		{paramAssetBucketARN, "ARN of the core bootstrap's asset bucket, so the edge reader is scoped to the objects in it the deploy tagged edge-readable.", in.refs.assetBucketARN},
	})

	userName, err := in.ns.EdgeUserNameFor(in.tier)
	if err != nil {
		return partStack{}, err
	}
	return newPartStack(cfn.Template{
		Description: "Ocel bootstrap part (" + CloudflareEdgePart + ", " + string(in.tier) + ") - what a Cloudflare front needs inside this AWS account: the IAM user it signs its calls with, scoped to this bootstrap alone.",
		Parameters:  params,
		Resources:   []cfn.Resource{edgeUserResource(in.ns, userName, in.tier)},
	}, values)
}

func edgeUserResource(ns Namespace, userName string, tier environment.Tier) cfn.Resource {
	invoke := []string{"lambda:InvokeFunctionUrl", "lambda:InvokeFunction"}
	return cfn.Resource{
		ID:   "EdgeUser",
		Type: "AWS::IAM::User",
		Metadata: cfn.Metadata{
			Description: "The identity the " + string(tier) + " edge signs its calls into this account with: it invokes app functions and the image optimizer, lists the asset bucket and reads the objects in it the deploy tagged edge-readable.",
		},
		Properties: map[string]any{
			"UserName": userName,
			"Policies": []any{map[string]any{
				"PolicyName": ns.PolicyName("edge-cache"),
				"PolicyDocument": iam.Document([]iam.Statement{
					{
						Actions:        invoke,
						ResourceValues: []any{cfn.Sub("arn:aws:lambda:*:${AWS::AccountId}:function:*")},
						Condition: map[string]any{"StringEquals": map[string]any{
							"aws:ResourceTag/ocel:component":          "function",
							"aws:ResourceTag/" + naming.EnvTierTagKey: string(tier),
						}},
					},
					{
						Actions:        []string{"s3:GetObject"},
						ResourceValues: []any{cfn.Sub("${" + paramAssetBucketARN + "}/*")},
						Condition: map[string]any{"StringEquals": map[string]any{
							"s3:ExistingObjectTag/" + naming.EdgeReadableTagKey: naming.EdgeReadableTagValue,
						}},
					},
					{Actions: []string{"s3:ListBucket"}, ResourceValues: []any{cfn.Ref(paramAssetBucketARN)}},
					{
						Actions:        invoke,
						ResourceValues: []any{cfn.Sub("arn:aws:lambda:${AWS::Region}:${AWS::AccountId}:function:" + imageoptimizer.NamesFor(string(ns), tier).Function)},
					},
				}),
			}},
		},
	}
}

func plannedEdgeCredentials(ctx context.Context, apis ParamAPIs, ns Namespace, tier environment.Tier, _ Request) ([]provider.Change, error) {
	names, err := edgeNamesFor(ns, tier, KindCloudflare)
	if err != nil {
		return nil, err
	}
	recorded, present, err := recordedEdgeKey(ctx, apis.SSM, names.credentialsParam)
	if err != nil {
		return nil, err
	}
	live, err := edgeKeyLive(ctx, apis.IAM, names.user, recorded.AccessKeyID)
	if err != nil {
		return nil, err
	}
	if live && recorded.Stale(time.Now()) {
		return []provider.Change{
			{Kind: kindParameter, Name: names.credentialsParam, Action: provider.ActionUpdate, Reason: keyStale},
			{Kind: kindAccessKey, Name: names.user, Action: provider.ActionUpdate, Reason: keyStale},
		}, nil
	}
	if live {
		return []provider.Change{
			{Kind: kindParameter, Name: names.credentialsParam, Action: provider.ActionKeep, Reason: paramCurrent},
			{Kind: kindAccessKey, Name: names.user, Action: provider.ActionKeep, Reason: paramCurrent},
		}, nil
	}
	credentials := provider.Change{Kind: kindParameter, Name: names.credentialsParam, Action: provider.ActionCreate}
	if present {
		credentials.Action, credentials.Reason = provider.ActionUpdate, keyGone
		if recorded.AccessKeyID == "" {
			credentials.Reason = keyUnrecorded
		}
	}
	return []provider.Change{
		credentials,
		{Kind: kindAccessKey, Name: names.user, Action: provider.ActionCreate},
	}, nil
}

func plannedCloudflareSever(ctx context.Context, apis ParamAPIs, ns Namespace, tier environment.Tier, _ Request) ([]provider.Change, error) {
	names, err := edgeNamesFor(ns, tier, KindCloudflare)
	if err != nil {
		return nil, err
	}
	reason := fmt.Sprintf(severedByRemove, CloudflareEdgePart, KindCloudflare)

	var changes []provider.Change
	keys, err := liveAccessKeys(ctx, apis.IAM, names.user)
	if err != nil {
		return nil, err
	}
	if len(keys) > 0 {
		changes = append(changes, provider.Change{
			Kind: kindAccessKey, Name: names.user, Action: provider.ActionDelete, Reason: reason,
		})
	}
	present, err := paramsPresent(ctx, apis.SSM, names.edgeParams())
	if err != nil {
		return nil, err
	}
	for _, param := range names.edgeParams() {
		if !present[param] {
			continue
		}
		changes = append(changes, provider.Change{
			Kind: kindParameter, Name: param, Action: provider.ActionDelete, Reason: reason,
		})
	}
	return changes, nil
}

func severCloudflareEdge(ctx context.Context, d stepDeps) error {
	names, err := edgeNamesFor(d.ns, d.tier, KindCloudflare)
	if err != nil {
		return err
	}
	d.progress.Say(fmt.Sprintf("Deleting the access key of edge reader %s", names.user))
	if err := deleteAccessKeys(ctx, d.iam, names.user); err != nil {
		return err
	}
	d.progress.Say(fmt.Sprintf("Deleting the %d parameters the %s edge was reached through (SSM)", len(names.edgeParams()), KindCloudflare))
	for _, param := range names.edgeParams() {
		if err := deleteParam(ctx, d.ssm, param); err != nil {
			return err
		}
	}
	return nil
}

func mintEdgeCredentials(ctx context.Context, d stepDeps) error {
	d.progress.Say("Ensuring the edge reader's credentials (SSM SecureString)")
	outcome, err := ensureEdgeCredentials(ctx, d.iam, d.ssm, d.ns, d.tier, KindCloudflare, time.Now())
	if err != nil {
		return err
	}
	if outcome.retired != "" {
		d.progress.Say(fmt.Sprintf("Retired the superseded edge reader access key %s, idle since every worker moved off it", outcome.retired))
	}
	switch {
	case outcome.rotated:
		d.progress.Warn(fmt.Sprintf("Rotated the edge reader access key, which was older than %d days: re-deploy each project so its worker signs with the new one; the next bootstrap retires the old key once it is idle", int(EdgeKeyMaxAge.Hours()/24)))
	case outcome.minted:
		d.progress.Say("Minted a new edge reader access key")
	default:
		d.progress.Debug("Reused the existing edge reader access key")
	}
	return nil
}
