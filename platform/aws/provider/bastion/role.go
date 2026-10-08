package bastion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"reflect"

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
	if !isManagedByOcel(got.Role.Tags, func(tag iamtypes.Tag) (*string, *string) { return tag.Key, tag.Value }) {
		return "", refuseUnowned("role", name)
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
	created, err := c.IAM.CreateRole(ctx, &iam.CreateRoleInput{
		RoleName:                 aws.String(name),
		Description:              aws.String("Ocel: the role a bastion task opens its Session Manager channels as, for the " + string(spec.Tier) + " tier"),
		AssumeRolePolicyDocument: aws.String(string(trust)),
		PermissionsBoundary:      aws.String(spec.Boundary),
		Tags:                     iamTagsFor(spec.Tier),
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
	document, err := json.Marshal(map[string]any{
		"Version":   "2012-10-17",
		"Statement": []map[string]any{{"Effect": "Allow", "Action": sessionChannelActions, "Resource": "*"}},
	})
	if err != nil {
		return err
	}
	got, err := c.IAM.GetRolePolicy(ctx, &iam.GetRolePolicyInput{RoleName: aws.String(role), PolicyName: aws.String(sessionPolicyName)})
	var missing *iamtypes.NoSuchEntityException
	switch {
	case err == nil:
		if isSamePolicy(aws.ToString(got.PolicyDocument), document) {
			return nil
		}
	case !errors.As(err, &missing):
		return fmt.Errorf("look up the session policy of role %s: %w", role, err)
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

func isSamePolicy(encoded string, want []byte) bool {
	decoded, err := url.QueryUnescape(encoded)
	if err != nil {
		return false
	}
	var current, wanted any
	if json.Unmarshal([]byte(decoded), &current) != nil || json.Unmarshal(want, &wanted) != nil {
		return false
	}
	return reflect.DeepEqual(current, wanted)
}
