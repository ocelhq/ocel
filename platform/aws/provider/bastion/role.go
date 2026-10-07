package bastion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/arn"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
)

var sessionChannelActions = []string{
	"ssmmessages:CreateControlChannel",
	"ssmmessages:CreateDataChannel",
	"ssmmessages:OpenControlChannel",
	"ssmmessages:OpenDataChannel",
}

func (c Clients) ensureTaskRole(ctx context.Context, spec Spec) (string, error) {
	name := NameFor(spec.Tier)
	arn, err := c.findTaskRole(ctx, name)
	if err != nil {
		return "", err
	}
	if arn == "" {
		if arn, err = c.createTaskRole(ctx, spec, name); err != nil {
			return "", err
		}
	}
	if err := c.ensureSessionPolicy(ctx, name); err != nil {
		return "", err
	}
	return arn, nil
}

func (c Clients) findTaskRole(ctx context.Context, name string) (string, error) {
	got, err := c.IAM.GetRole(ctx, &iam.GetRoleInput{RoleName: aws.String(name)})
	var missing *iamtypes.NoSuchEntityException
	if errors.As(err, &missing) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("look up role %s: %w", name, err)
	}
	owned := slices.ContainsFunc(got.Role.Tags, func(tag iamtypes.Tag) bool {
		return aws.ToString(tag.Key) == managedByTagKey && aws.ToString(tag.Value) == managedByTagValue
	})
	if !owned {
		return "", fmt.Errorf("role %s exists and is not tagged %s=%s, so Ocel will not run a task as it", name, managedByTagKey, managedByTagValue)
	}
	return aws.ToString(got.Role.Arn), nil
}

func (c Clients) createTaskRole(ctx context.Context, spec Spec, name string) (string, error) {
	boundary, err := arn.Parse(spec.Boundary)
	if err != nil {
		return "", fmt.Errorf("the app boundary %q is no ARN, so the bastion's task role cannot name the account it trusts: %w", spec.Boundary, err)
	}
	trust, err := json.Marshal(map[string]any{
		"Version": "2012-10-17",
		"Statement": []map[string]any{{
			"Effect":    "Allow",
			"Principal": map[string]any{"Service": ecsTasksPrincipal},
			"Action":    "sts:AssumeRole",
			"Condition": map[string]any{
				"StringEquals": map[string]string{"aws:SourceAccount": boundary.AccountID},
				"ArnLike":      map[string]string{"aws:SourceArn": "arn:" + boundary.Partition + ":ecs:*:" + boundary.AccountID + ":*"},
			},
		}},
	})
	if err != nil {
		return "", err
	}
	var roleTags []iamtypes.Tag
	for _, tag := range ecsTags(tags(spec.Tier)) {
		roleTags = append(roleTags, iamtypes.Tag{Key: tag.Key, Value: tag.Value})
	}
	created, err := c.IAM.CreateRole(ctx, &iam.CreateRoleInput{
		RoleName:                 aws.String(name),
		Description:              aws.String("Ocel: the role a bastion task opens its Session Manager channels as, for the " + string(spec.Tier) + " tier"),
		AssumeRolePolicyDocument: aws.String(string(trust)),
		PermissionsBoundary:      aws.String(spec.Boundary),
		Tags:                     roleTags,
	})
	var exists *iamtypes.EntityAlreadyExistsException
	if errors.As(err, &exists) {
		return c.findTaskRole(ctx, name)
	}
	if err != nil {
		return "", fmt.Errorf("create role %s: %w", name, err)
	}
	return aws.ToString(created.Role.Arn), nil
}

func (c Clients) ensureSessionPolicy(ctx context.Context, role string) error {
	_, err := c.IAM.GetRolePolicy(ctx, &iam.GetRolePolicyInput{RoleName: aws.String(role), PolicyName: aws.String(sessionPolicyName)})
	var missing *iamtypes.NoSuchEntityException
	if err == nil {
		return nil
	}
	if !errors.As(err, &missing) {
		return fmt.Errorf("look up the session policy of role %s: %w", role, err)
	}
	document, err := json.Marshal(map[string]any{
		"Version":   "2012-10-17",
		"Statement": []map[string]any{{"Effect": "Allow", "Action": sessionChannelActions, "Resource": "*"}},
	})
	if err != nil {
		return err
	}
	if _, err := c.IAM.PutRolePolicy(ctx, &iam.PutRolePolicyInput{
		RoleName:       aws.String(role),
		PolicyName:     aws.String(sessionPolicyName),
		PolicyDocument: aws.String(string(document)),
	}); err != nil {
		return fmt.Errorf("put the session policy on role %s: %w", role, err)
	}
	return nil
}
