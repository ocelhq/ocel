package bootstrap

import (
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

func SiblingClassOf(class string) (string, error) {
	switch class {
	case ClassProduction:
		return ClassPreview, nil
	case ClassPreview:
		return ClassProduction, nil
	default:
		return "", fmt.Errorf("bootstrap: unknown class %q", class)
	}
}

func ClassParamNames(ns Namespace, class string) ([]string, error) {
	secret, err := ns.OriginSecretParamFor(class)
	if err != nil {
		return nil, err
	}
	var params []string
	for _, kind := range edgeKinds() {
		names, err := edgeNamesFor(ns, class, kind)
		if err != nil {
			return nil, err
		}
		params = append(params, names.edgeParams()...)
	}
	return append(params, secret), nil
}

func PassphraseHeldBySibling(ctx context.Context, api cfn.Describer, ns Namespace, class string) (bool, error) {
	sibling, err := SiblingClassOf(class)
	if err != nil {
		return false, err
	}
	stackName, err := ns.StackNameFor(sibling)
	if err != nil {
		return false, err
	}
	out, err := cfn.StackOutputs(ctx, api, stackName)
	if err != nil {
		return false, err
	}
	return out != nil, nil
}

func featureStackNames(ns Namespace, names []string, class string) []string {
	out := make([]string, 0, len(names))
	for _, name := range names {
		out = append(out, ns.FeatureStackName(name, class))
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

func deleteFeatureStacks(ctx context.Context, stacks cfn.TeardownAPI, ns Namespace, class string, names []string, log func(string)) error {
	order, err := FeatureDeleteOrder(names)
	if err != nil {
		return err
	}
	for _, name := range order {
		stackName := ns.FeatureStackName(name, class)
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
		if log != nil {
			log(fmt.Sprintf("removed %s", stackName))
		}
	}
	return nil
}

func Teardown(ctx context.Context, apis TeardownAPIs, ns Namespace, class string, progress, log func(string)) error {
	report := func(f func(string), msg string) {
		if f != nil {
			f(msg)
		}
	}

	stackName, err := ns.StackNameFor(class)
	if err != nil {
		return err
	}
	userName, err := ns.EdgeUserNameFor(class)
	if err != nil {
		return err
	}
	params, err := ClassParamNames(ns, class)
	if err != nil {
		return err
	}

	deployed, _, err := readBootstrap(ctx, apis.CFN, ns, class)
	if err != nil {
		return err
	}

	if deployed.Present {
		report(progress, fmt.Sprintf("Deleting the access key of edge reader %s", userName))
		if err := deleteAccessKeys(ctx, apis.IAM, userName); err != nil {
			return err
		}

		for _, bucket := range []string{deployed.StateBucket, deployed.ArtifactBucket, deployed.AssetBucket} {
			if bucket == "" {
				continue
			}
			report(progress, fmt.Sprintf("Emptying %s", bucket))
			if err := cfn.EmptyBucket(ctx, apis.Buckets, bucket); err != nil {
				return err
			}
		}

		standing, err := standingFeatures(ctx, apis.CFN, ns, class)
		if err != nil {
			return err
		}
		present := standing.Names()
		if len(present) > 0 {
			report(progress, fmt.Sprintf("Deleting %s (CloudFormation)", strings.Join(featureStackNames(ns, present, class), ", ")))
			if err := deleteFeatureStacks(ctx, apis.CFN, ns, class, present, func(msg string) { report(log, msg) }); err != nil {
				return err
			}
		}

		report(progress, fmt.Sprintf("Deleting %s (CloudFormation)", ns.runtimeStackName(class)))
		if err := deleteRuntimeLayerStack(ctx, apis.CFN, ns, class, func(msg string) { report(log, msg) }); err != nil {
			return err
		}

		if deployed.AppBoundaryARN != "" {
			report(progress, "Releasing the app boundary from every role still under it")
			if err := releaseAppBoundary(ctx, apis.IAM, deployed.AppBoundaryARN, func(msg string) { report(log, msg) }); err != nil {
				return err
			}
		}

		report(progress, fmt.Sprintf("Deleting %s (CloudFormation)", stackName))
		if err := cfn.Delete(ctx, apis.CFN, stackName); err != nil {
			return err
		}
	} else {
		report(log, fmt.Sprintf("no %s stack in this account; only the parameters it left behind are removed", stackName))
	}

	report(progress, "Deleting the bootstrap's stored parameters (SSM)")
	shared, err := PassphraseHeldBySibling(ctx, apis.CFN, ns, class)
	if err != nil {
		return err
	}
	if shared {
		report(log, fmt.Sprintf("the %s bootstrap still stands and its Pulumi state is encrypted under the shared passphrase in %s; it stays", siblingName(class), ns.PassphraseParamName()))
	} else {
		report(log, fmt.Sprintf("%s stays: it is the only copy of the passphrase every Pulumi stack this account ever held was encrypted under, and no Ocel credential may delete it. Once nothing encrypted under it remains, remove it with `aws ssm delete-parameter --name %s`", ns.PassphraseParamName(), ns.PassphraseParamName()))
	}
	for _, name := range params {
		if err := deleteParam(ctx, apis.SSM, name); err != nil {
			return err
		}
	}
	return nil
}

func siblingName(class string) string {
	sibling, err := SiblingClassOf(class)
	if err != nil {
		return ""
	}
	return sibling
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

func releaseAppBoundary(ctx context.Context, iamClient IAMBoundaryAPI, policyARN string, log func(string)) error {
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
			log(fmt.Sprintf("released the app boundary from role %s; it keeps running under its own policies alone", name))
		}
		if !out.IsTruncated {
			return nil
		}
		marker = out.Marker
	}
}
