package bootstrap

import (
	"context"
	"fmt"
	"time"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
)

const KindCloudflare = "cloudflare"

var cloudflareEdgeFeature = feature{
	name:       FeatureCloudflareEdge,
	summary:    "Cloudflare as the front — workers and the credential it reads the asset bucket with",
	dependsOn:  []string{FeatureISR},
	edges:      []edge.Kind{KindCloudflare},
	template:   cloudflareEdgeTemplate,
	payloads:   noPayloads,
	placements: noPlacements,
	after:      mintEdgeCredentials,
	afterPlan:  plannedEdgeCredentials,
	drop:       severCloudflareEdge,
	dropPlan:   plannedCloudflareSever,
}

func cloudflareEdgeTemplate(in featureInputs) (featureStack, error) {
	specs := []crossStackParam{
		{paramAssetBucketARN, "ARN of the core bootstrap's asset bucket, so the edge reader is scoped to the objects in it the deploy tagged edge-readable.", in.refs.assetBucketARN},
	}
	optimizer := in.alongside.Has(FeatureImageOptimization)
	if optimizer {
		specs = append(specs, crossStackParam{paramImageOptimizerARN, "ARN of the shared image optimizer, the one function the edge reader may invoke for an image.", in.refs.imageOptimizerARN})
	}
	params, values := crossStack(specs)

	userName, err := in.ns.EdgeUserNameFor(in.tier)
	if err != nil {
		return featureStack{}, err
	}
	return featureStack{
		params: values,
		body: fmt.Sprintf(`AWSTemplateFormatVersion: '2010-09-09'
Description: "Ocel bootstrap feature (%s, %s) - what a Cloudflare front needs inside this AWS account: the IAM user it signs its calls with, scoped to this bootstrap alone."
%sResources:
%s`,
			FeatureCloudflareEdge, in.tier, params,
			edgeUserResource(in.ns, userName, in.tier, optimizer)),
	}, nil
}

func edgeUserResource(ns Namespace, userName string, tier environment.Tier, optimizer bool) string {
	invoke := ""
	if optimizer {
		invoke = imageOptimizerInvokeStatement()
	}
	return fmt.Sprintf(`  EdgeUser:
    Type: AWS::IAM::User
    Metadata:
      Description: "The identity the %s edge signs its calls into this account with: it invokes app functions, lists the asset bucket and reads the objects in it the deploy tagged edge-readable."
    Properties:
      UserName: %s
      Policies:
        - PolicyName: %s
          PolicyDocument:
            Version: '2012-10-17'
            Statement:
              - Effect: Allow
                Action:
                  - lambda:InvokeFunctionUrl
                  - lambda:InvokeFunction
                Resource: !Sub 'arn:aws:lambda:*:${AWS::AccountId}:function:*'
                Condition:
                  StringEquals:
                    'aws:ResourceTag/ocel:component': 'function'
                    'aws:ResourceTag/%s': '%s'
              - Effect: Allow
                Action: s3:GetObject
                Resource: !Sub '${%s}/*'
                Condition:
                  StringEquals:
                    's3:ExistingObjectTag/%s': '%s'
              - Effect: Allow
                Action: s3:ListBucket
                Resource: !Ref %s
%s`, tier, userName, ns.PolicyName("edge-cache"),
		naming.EnvTierTagKey, tier,
		paramAssetBucketARN, naming.EdgeReadableTagKey, naming.EdgeReadableTagValue, paramAssetBucketARN,
		invoke)
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
	reason := fmt.Sprintf(severedByRemove, FeatureCloudflareEdge, KindCloudflare)

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
