package component

import (
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

func NewServiceRole(ctx *pulumi.Context, name, description, servicePrincipal, boundaryARN, rolePath string, tags map[string]string, statements map[string]PolicyStatement) (*awsiam.Role, error) {
	role, err := awsiam.NewRole(ctx, name, &awsiam.RoleArgs{
		AssumeRolePolicy:    pulumi.String(iam.AssumeRolePolicy(servicePrincipal)),
		Description:         pulumi.String(description),
		PermissionsBoundary: pulumi.String(boundaryARN),
		Path:                pulumi.String(rolePath),
		Tags:                Tags(naming.KindRole, "", tags),
	})
	if err != nil {
		return nil, err
	}
	for stmtName, stmt := range statements {
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

		if _, err := awsiam.NewRolePolicy(ctx, name+"-policy-"+stmtName, &awsiam.RolePolicyArgs{
			Role:   role.ID(),
			Policy: policyJSON,
		}); err != nil {
			return nil, err
		}
	}
	return role, nil
}
