package bootstrap

import (
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/platform/aws/provider/cfn"

	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
)

type IAMKeyAPI interface {
	ListAccessKeys(ctx context.Context, in *iam.ListAccessKeysInput, optFns ...func(*iam.Options)) (*iam.ListAccessKeysOutput, error)
	DeleteAccessKey(ctx context.Context, in *iam.DeleteAccessKeyInput, optFns ...func(*iam.Options)) (*iam.DeleteAccessKeyOutput, error)
}

type IAMBoundaryAPI interface {
	ListEntitiesForPolicy(ctx context.Context, in *iam.ListEntitiesForPolicyInput, optFns ...func(*iam.Options)) (*iam.ListEntitiesForPolicyOutput, error)
	DeleteRolePermissionsBoundary(ctx context.Context, in *iam.DeleteRolePermissionsBoundaryInput, optFns ...func(*iam.Options)) (*iam.DeleteRolePermissionsBoundaryOutput, error)
}

type IAMTeardownAPI interface {
	IAMKeyAPI
	IAMBoundaryAPI
}

type TeardownAPIs struct {
	CFN     cfn.TeardownAPI
	SSM     SSMAPI
	IAM     IAMTeardownAPI
	Buckets cfn.BucketEmptierAPI
}

func TierParamNames(ns Namespace, tier environment.Tier) ([]string, error) {
	secret, err := ns.OriginSecretParamFor(tier)
	if err != nil {
		return nil, err
	}
	var params []string
	for _, kind := range edgeKinds() {
		names, err := edgeNamesFor(ns, tier, kind)
		if err != nil {
			return nil, err
		}
		params = append(params, names.edgeParams()...)
	}
	return append(params, secret), nil
}

func SiblingSharesPassphrase(ctx context.Context, api cfn.StacksAPI, ns Namespace, tier environment.Tier) (bool, error) {
	stackName, err := ns.StackNameFor(tier.Sibling())
	if err != nil {
		return false, err
	}
	out, err := cfn.StackOutputs(ctx, api, stackName)
	if err != nil {
		return false, err
	}
	return out != nil, nil
}

func featureStackNames(ns Namespace, names []string, tier environment.Tier) []string {
	out := make([]string, 0, len(names))
	for _, name := range names {
		out = append(out, ns.FeatureStackName(name, tier))
	}
	return out
}

func FeatureDeleteOrder(names []string) ([]string, error) {
	levels, err := featureLevels(names)
	if err != nil {
		return nil, err
	}
	var out []string
	for i := len(levels) - 1; i >= 0; i-- {
		out = append(out, levels[i]...)
	}
	return out, nil
}

func deleteFeatureStacks(ctx context.Context, stacks cfn.TeardownAPI, ns Namespace, tier environment.Tier, names []string, progress progress.Progress) error {
	order, err := FeatureDeleteOrder(names)
	if err != nil {
		return err
	}
	for _, name := range order {
		stackName := ns.FeatureStackName(name, tier)
		out, err := cfn.StackOutputs(ctx, stacks, stackName)
		if err != nil {
			return err
		}
		if out == nil {
			continue
		}
		if err := cfn.Delete(ctx, stacks, stackName); err != nil {
			return err
		}
		progress.Say("Removed stack " + stackName)
	}
	return nil
}

func Teardown(ctx context.Context, apis TeardownAPIs, ns Namespace, tier environment.Tier, progress progress.Progress) error {
	progress = ensureProgress(progress)

	stackName, err := ns.StackNameFor(tier)
	if err != nil {
		return err
	}
	userName, err := ns.EdgeUserNameFor(tier)
	if err != nil {
		return err
	}
	params, err := TierParamNames(ns, tier)
	if err != nil {
		return err
	}

	core, err := cfn.DescribeStack(ctx, apis.CFN, stackName)
	if err != nil {
		return err
	}
	deployed, _, err := readBootstrap(ctx, apis.CFN, ns, tier)
	if err != nil {
		return err
	}
	switch {
	case core == nil:
		progress.Say(fmt.Sprintf("No stack %s in this account, so only what exists beside it is removed", stackName))
	case !deployed.Present:
		progress.Warn(fmt.Sprintf("Stack %s is %s and names none of its resources, so only what exists beside it is removed", stackName, core.StackStatus))
	}

	progress.Say(fmt.Sprintf("Deleting the access key of edge reader %s", userName))
	if err := deleteAccessKeys(ctx, apis.IAM, userName); err != nil {
		return err
	}

	for _, bucket := range []string{deployed.StateBucket, deployed.ArtifactBucket, deployed.AssetBucket} {
		if bucket == "" {
			continue
		}
		progress.Say("Emptying bucket " + bucket)
		if err := cfn.EmptyBucket(ctx, apis.Buckets, bucket); err != nil {
			return err
		}
	}

	installed, err := installedFeatures(ctx, apis.CFN, ns, tier)
	if err != nil {
		return err
	}
	present := installed.Names()
	if len(present) > 0 {
		progress.Say(fmt.Sprintf("Deleting %s (CloudFormation)", strings.Join(featureStackNames(ns, present, tier), ", ")))
		if err := deleteFeatureStacks(ctx, apis.CFN, ns, tier, present, progress); err != nil {
			return err
		}
	}

	progress.Say(fmt.Sprintf("Deleting %s (CloudFormation)", ns.runtimeStackName(tier)))
	if err := deleteRuntimeLayerStack(ctx, apis.CFN, ns, tier, progress); err != nil {
		return err
	}

	if deployed.AppBoundaryARN != "" {
		progress.Say("Releasing the app boundary from every role still under it")
		if err := releaseAppBoundary(ctx, apis.IAM, deployed.AppBoundaryARN, progress); err != nil {
			return err
		}
	}

	if core != nil {
		progress.Say(fmt.Sprintf("Deleting %s (CloudFormation)", stackName))
		if err := cfn.Delete(ctx, apis.CFN, stackName); err != nil {
			return err
		}
	}

	progress.Say(fmt.Sprintf("Deleting the %s bootstrap's stored parameters (SSM)", tier))
	shared, err := SiblingSharesPassphrase(ctx, apis.CFN, ns, tier)
	if err != nil {
		return err
	}
	if shared {
		progress.Say(fmt.Sprintf("Keeping the Pulumi passphrase in %s: the %s bootstrap is still installed and its Pulumi state is encrypted under it", ns.PassphraseParamName(), tier.Sibling()))
	} else {
		params = append(params, ns.PassphraseParamName())
	}
	for _, name := range params {
		if err := deleteParam(ctx, apis.SSM, name); err != nil {
			return err
		}
	}
	return nil
}

func deleteParam(ctx context.Context, ssmClient SSMAPI, name string) error {
	if _, err := ssmClient.DeleteParameter(ctx, &ssm.DeleteParameterInput{Name: aws.String(name)}); err != nil {
		var notFound *ssmtypes.ParameterNotFound
		if errors.As(err, &notFound) {
			return nil
		}
		return fmt.Errorf("delete parameter %s: %w", name, err)
	}
	return nil
}

func deleteAccessKeys(ctx context.Context, iamClient IAMKeyAPI, userName string) error {
	out, err := iamClient.ListAccessKeys(ctx, &iam.ListAccessKeysInput{UserName: aws.String(userName)})
	if err != nil {
		var noUser *iamtypes.NoSuchEntityException
		if errors.As(err, &noUser) {
			return nil
		}
		return fmt.Errorf("list access keys for %s: %w", userName, err)
	}
	for _, key := range out.AccessKeyMetadata {
		if _, err := iamClient.DeleteAccessKey(ctx, &iam.DeleteAccessKeyInput{
			UserName:    aws.String(userName),
			AccessKeyId: key.AccessKeyId,
		}); err != nil {
			var noKey *iamtypes.NoSuchEntityException
			if errors.As(err, &noKey) {
				continue
			}
			return fmt.Errorf("delete access key %s of %s: %w", aws.ToString(key.AccessKeyId), userName, err)
		}
	}
	return nil
}

func releaseAppBoundary(ctx context.Context, iamClient IAMBoundaryAPI, policyARN string, progress progress.Progress) error {
	var marker *string
	for {
		out, err := iamClient.ListEntitiesForPolicy(ctx, &iam.ListEntitiesForPolicyInput{
			PolicyArn:         aws.String(policyARN),
			EntityFilter:      iamtypes.EntityTypeRole,
			PolicyUsageFilter: iamtypes.PolicyUsageTypePermissionsBoundary,
			Marker:            marker,
		})
		if err != nil {
			var noPolicy *iamtypes.NoSuchEntityException
			if errors.As(err, &noPolicy) {
				return nil
			}
			return fmt.Errorf("list the roles under %s: %w", policyARN, err)
		}
		for _, role := range out.PolicyRoles {
			name := aws.ToString(role.RoleName)
			if _, err := iamClient.DeleteRolePermissionsBoundary(ctx, &iam.DeleteRolePermissionsBoundaryInput{RoleName: aws.String(name)}); err != nil {
				var noRole *iamtypes.NoSuchEntityException
				if errors.As(err, &noRole) {
					continue
				}
				return fmt.Errorf("release the app boundary from role %s: %w", name, err)
			}
			progress.Say(fmt.Sprintf("Released the app boundary from role %s, which keeps running under its own policies alone", name))
		}
		if !out.IsTruncated {
			return nil
		}
		marker = out.Marker
	}
}
