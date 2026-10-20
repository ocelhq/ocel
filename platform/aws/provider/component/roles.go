package component

import (
	"maps"
	"slices"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/platform/aws/provider/iam"
	awsiam "github.com/pulumi/pulumi-aws/sdk/v7/go/aws/iam"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

type PolicyStatement struct {
	Actions   []string
	Resources []pulumi.StringInput
	Condition map[string]any
}

func NewServiceRole(ctx *pulumi.Context, name Name, description, servicePrincipal, boundaryARN, rolePath string, tags map[string]string, statements map[string]PolicyStatement) (*awsiam.Role, error) {
	role, err := awsiam.NewRole(ctx, name.Pulumi, &awsiam.RoleArgs{
		Name:                name.AWS,
		AssumeRolePolicy:    pulumi.String(iam.AssumeRolePolicy(servicePrincipal)),
		Description:         pulumi.String(description),
		PermissionsBoundary: pulumi.String(boundaryARN),
		Path:                pulumi.String(rolePath),
		Tags:                Tags(naming.KindRole, "", tags),
	})
	if err != nil {
		return nil, err
	}
	for _, stmtName := range slices.Sorted(maps.Keys(statements)) {
		stmt := statements[stmtName]
		actions, condition := stmt.Actions, stmt.Condition
		resourceInputs := make([]any, len(stmt.Resources))
		for i, r := range stmt.Resources {
			resourceInputs[i] = r
		}
		policyJSON := pulumi.All(resourceInputs...).ApplyT(func(vs []any) (string, error) {
			resources := make([]string, len(vs))
			for i, v := range vs {
				resources[i], _ = v.(string)
			}
			return iam.InlinePolicy(actions, resources, condition)
		}).(pulumi.StringOutput)

		if _, err := NewRolePolicy(ctx, name, role, stmtName, policyJSON); err != nil {
			return nil, err
		}
	}
	return role, nil
}

func NewRolePolicy(ctx *pulumi.Context, roleName Name, role *awsiam.Role, policy string, document pulumi.StringInput, opts ...pulumi.ResourceOption) (*awsiam.RolePolicy, error) {
	return awsiam.NewRolePolicy(ctx, roleName.Part(policy), &awsiam.RolePolicyArgs{
		Name:   pulumi.String(policy),
		Role:   role.Name,
		Policy: document,
	}, opts...)
}

func AttachManagedPolicy(ctx *pulumi.Context, roleName Name, role *awsiam.Role, policyARN string) (*awsiam.RolePolicyAttachment, error) {
	return awsiam.NewRolePolicyAttachment(ctx, roleName.Attachment(policyARN), &awsiam.RolePolicyAttachmentArgs{
		Role:      role.Name,
		PolicyArn: pulumi.String(policyARN),
	})
}
