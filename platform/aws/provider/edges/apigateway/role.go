package apigateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const invokePolicyName = "ocel-edge-invoke"

func invokeRoleName(class edge.Class) string {
	if class == edge.ClassPreview {
		return "ocel-edge-invoke-preview"
	}
	return "ocel-edge-invoke"
}

func ensureInvokeRole(ctx context.Context, c Clients, class edge.Class, assetBucket string) (string, error) {
	name := invokeRoleName(class)
	arn, found, err := findRole(ctx, c, name)
	if err != nil {
		return "", err
	}
	if !found {
		trust, err := json.Marshal(map[string]any{
			"Version": "2012-10-17",
			"Statement": []any{map[string]any{
				"Effect":    "Allow",
				"Principal": map[string]any{"Service": "apigateway.amazonaws.com"},
				"Action":    "sts:AssumeRole",
			}},
		})
		if err != nil {
			return "", fmt.Errorf("render the trust policy for %s: %w", name, err)
		}
		out, err := c.IAM.CreateRole(ctx, &iam.CreateRoleInput{
			RoleName:                 aws.String(name),
			AssumeRolePolicyDocument: aws.String(string(trust)),
			Description:              aws.String("API Gateway assumes this role to invoke Ocel's entry functions and read a release's static assets."),
		})
		if err != nil {
			return "", fmt.Errorf("create the %s role API Gateway invokes through: %w", name, err)
		}
		arn = aws.ToString(out.Role.Arn)
	}

	policy, err := invokePolicy(c.Region, accountOf(arn), assetBucket)
	if err != nil {
		return "", err
	}
	if _, err := c.IAM.PutRolePolicy(ctx, &iam.PutRolePolicyInput{
		RoleName:       aws.String(name),
		PolicyName:     aws.String(invokePolicyName),
		PolicyDocument: aws.String(policy),
	}); err != nil {
		return "", fmt.Errorf("grant %s what API Gateway invokes through it: %w", name, err)
	}
	return arn, nil
}

func requireInvokeRole(ctx context.Context, c Clients, class edge.Class) (string, error) {
	name := invokeRoleName(class)
	arn, found, err := findRole(ctx, c, name)
	if err != nil {
		return "", err
	}
	if !found {
		return "", fmt.Errorf("the %s role API Gateway invokes this project's functions through does not exist in this account, so the %q edge has nothing to front the deployment with. It is created once per account when you bootstrap, and this deploy will not create it: run `%s` and deploy again", name, Kind, bootstrapCommandFor(class))
	}
	return arn, nil
}

func bootstrapCommandFor(class edge.Class) string {
	if class == edge.ClassPreview {
		return "ocel bootstrap --preview"
	}
	return "ocel bootstrap"
}

func findRole(ctx context.Context, c Clients, name string) (string, bool, error) {
	out, err := c.IAM.GetRole(ctx, &iam.GetRoleInput{RoleName: aws.String(name)})
	if err != nil {
		var missing *iamtypes.NoSuchEntityException
		if errors.As(err, &missing) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("read the %s role: %w", name, err)
	}
	return aws.ToString(out.Role.Arn), true, nil
}

func invokePolicy(region, account, assetBucket string) (string, error) {
	if region == "" || account == "" {
		return "", fmt.Errorf("render the invoke policy: it grants API Gateway the functions of one account in one region, and this deploy names region %q and account %q", region, account)
	}
	statements := []any{map[string]any{
		"Effect":   "Allow",
		"Action":   "lambda:InvokeFunction",
		"Resource": fmt.Sprintf("arn:aws:lambda:%s:%s:function:*", region, account),
	}}
	if assetBucket != "" {
		statements = append(statements, map[string]any{
			"Effect":   "Allow",
			"Action":   "s3:GetObject",
			"Resource": "arn:aws:s3:::" + assetBucket + "/*",
		})
	}
	encoded, err := json.Marshal(map[string]any{"Version": "2012-10-17", "Statement": statements})
	if err != nil {
		return "", fmt.Errorf("render the invoke policy: %w", err)
	}
	return string(encoded), nil
}

func deleteInvokeRole(ctx context.Context, c Clients, class edge.Class) error {
	name := invokeRoleName(class)
	if _, err := c.IAM.DeleteRolePolicy(ctx, &iam.DeleteRolePolicyInput{
		RoleName:   aws.String(name),
		PolicyName: aws.String(invokePolicyName),
	}); err != nil {
		var missing *iamtypes.NoSuchEntityException
		if !errors.As(err, &missing) {
			return fmt.Errorf("drop the invoke policy from %s: %w", name, err)
		}
	}
	if _, err := c.IAM.DeleteRole(ctx, &iam.DeleteRoleInput{RoleName: aws.String(name)}); err != nil {
		var missing *iamtypes.NoSuchEntityException
		if !errors.As(err, &missing) {
			return fmt.Errorf("delete the %s role: %w", name, err)
		}
	}
	return nil
}
