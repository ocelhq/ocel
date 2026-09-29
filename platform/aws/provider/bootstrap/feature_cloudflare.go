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
	summary:    "Cloudflare as the front — workers, credential, snapshot publisher",
	dependsOn:  []string{FeatureISR},
	edges:      []edge.Kind{KindCloudflare},
	template:   cloudflareEdgeTemplate,
	payloads:   cloudflareEdgePayloads,
	placements: cloudflareEdgePlacements,
	after:      mintEdgeCredentials,
	afterPlan:  plannedEdgeCredentials,
	drop:       severCloudflareEdge,
	dropPlan:   plannedCloudflareSever,
}

func cloudflareEdgePayloads(ctx context.Context, store ObjectStore, bucket string) (stackPayloads, error) {
	var code stackPayloads
	var err error
	code.publisher, err = ensureTagPublisherPayload(ctx, store, bucket)
	return code, err
}

func cloudflareEdgePlacements(bucket string) stackPayloads {
	return stackPayloads{publisher: tagPublisherPlacement(bucket)}
}

func cloudflareEdgeTemplate(in featureInputs) featureStack {
	specs := []crossStackParam{
		{paramAssetBucketName, "The core bootstrap's asset bucket, which the edge reads static assets from and writes its fetch cache back into.", in.refs.assetBucket},
		{paramAssetBucketARN, "ARN of that bucket, so the edge reader is granted the asset and fetch-cache prefixes and nothing else.", in.refs.assetBucketARN},
		{paramStateTableARN, "ARN of the core bootstrap's state table, so the edge reader reaches tag items alone.", in.refs.stateTableARN},
		{paramStateTableStreamARN, "ARN of that table's stream, the only trigger the tag publisher has.", in.refs.stateTableStreamARN},
		{paramRevalidateQueueARN, "ARN of the revalidation queue the ISR feature provisioned, the one queue the edge reader may enqueue a refresh on.", in.refs.revalidateQueueARN},
	}
	optimizer := in.alongside.Has(FeatureImageOptimization)
	if optimizer {
		specs = append(specs, crossStackParam{paramImageOptimizerARN, "ARN of the shared image optimizer, the one function the edge reader may invoke for an image.", in.refs.imageOptimizerARN})
	}
	params, values := crossStack(specs)

	userName, _ := in.ns.EdgeUserNameFor(in.tier)
	return featureStack{
		params: values,
		body: fmt.Sprintf(`AWSTemplateFormatVersion: '2010-09-09'
Description: "Ocel bootstrap feature (%s, %s) - what a Cloudflare front needs inside this AWS account: the IAM user it signs its calls with, scoped to this bootstrap alone, and the publisher that sends each build's tag snapshot out to the edge's ISR writer."
%sResources:
%s%s`,
			FeatureCloudflareEdge, in.tier, params,
			edgeUserResource(in.ns, userName, in.tier, optimizer),
			tagPublisherResources(in.ns, in.code.publisher, in.tier)),
	}
}

func edgeUserResource(ns Namespace, userName string, tier environment.Tier, optimizer bool) string {
	invoke := ""
	if optimizer {
		invoke = imageOptimizerInvokeStatement()
	}
	return fmt.Sprintf(`  EdgeUser:
    Type: AWS::IAM::User
    Metadata:
      Description: "The identity the %s edge signs its calls into this account with: it reads the asset bucket, writes the fetch cache back, reads and updates tag items, invokes app functions and enqueues ISR revalidations."
    Properties:
      UserName: %s
      Policies:
        - PolicyName: %s
          PolicyDocument:
            Version: '2012-10-17'
            Statement:
              - Effect: Allow
                Action: s3:GetObject
                Resource: !Sub '${%s}/*'
              - Effect: Allow
                Action: s3:PutObject
                Resource: !Sub '${%s}/*/fetch-cache/*.cache.json'
              - Effect: Allow
                Action:
                  - dynamodb:BatchGetItem
                  - dynamodb:UpdateItem
                Resource: !Ref %s
                Condition:
                  ForAllValues:StringLike:
                    dynamodb:LeadingKeys:
                      - 'PROJECT#*#TAG#*'
              - Effect: Allow
                Action: dynamodb:Query
                Resource: !Sub '${%s}/index/%s'
                Condition:
                  ForAllValues:StringLike:
                    dynamodb:LeadingKeys:
                      - 'PROJECT#*#TAG#*'
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
                Action: sqs:SendMessage
                Resource: !Ref %s
              - Effect: Allow
                Action:
                  - kms:GenerateDataKey
                  - kms:Decrypt
                Resource: '*'
                Condition:
                  StringEquals:
                    kms:ViaService: !Sub 'sqs.${AWS::Region}.amazonaws.com'
%s`, tier, userName, ns.PolicyName("edge-cache"),
		paramAssetBucketARN, paramAssetBucketARN,
		paramStateTableARN, paramStateTableARN, StateTableIndexName, naming.EnvTierTagKey, tier,
		paramRevalidateQueueARN, invoke)
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
