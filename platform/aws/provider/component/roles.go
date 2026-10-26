package component

import (
	"fmt"
	"maps"
	"slices"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/aws/provider/iam"
	awsiam "github.com/pulumi/pulumi-aws/sdk/v7/go/aws/iam"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

const (
	TasksPolicyName     = "tasks"
	VariablesPolicyName = "variables-read"
	bindingPolicyWord   = "binding"
)

func BindingPolicyName(binding string) string {
	return naming.Join(naming.WordSeparator, bindingPolicyWord, binding)
}

type PolicyStatement struct {
	Actions   []string
	Resources []pulumi.StringInput
	Condition map[string]any
}

func (stmt PolicyStatement) Document() pulumi.StringOutput {
	resources := make([]any, len(stmt.Resources))
	for i, r := range stmt.Resources {
		resources[i] = r
	}
	return pulumi.All(resources...).ApplyT(func(vs []any) (string, error) {
		named := make([]string, len(vs))
		for i, v := range vs {
			named[i], _ = v.(string)
		}
		return iam.InlinePolicy(stmt.Actions, named, stmt.Condition)
	}).(pulumi.StringOutput)
}

type AppRole struct {
	Name        Name
	Description pulumi.StringInput
	Principal   string
	Boundary    string
	Path        string
	Tags        map[string]string
	Managed     []string
	Policies    map[string]pulumi.StringInput
}

func NewAppRole(ctx *pulumi.Context, r AppRole) (*awsiam.Role, []pulumi.Resource, error) {
	if r.Boundary == "" {
		return nil, nil, fmt.Errorf("mint the role %s: this deploy resolved no app boundary, and a role made without one is capped by nothing", r.Name.Pulumi)
	}
	role, err := awsiam.NewRole(ctx, r.Name.Pulumi, &awsiam.RoleArgs{
		Name:                r.Name.AWS,
		Description:         r.Description,
		AssumeRolePolicy:    pulumi.String(iam.AssumeRolePolicy(r.Principal)),
		PermissionsBoundary: pulumi.String(r.Boundary),
		Path:                pulumi.String(r.Path),
		Tags:                Tags(naming.KindRole, "", r.Tags),
	})
	if err != nil {
		return nil, nil, err
	}
	granted := make([]pulumi.Resource, 0, len(r.Managed)+len(r.Policies))
	attachedAs := map[string]string{}
	for _, policyARN := range r.Managed {
		name := r.Name.Attachment(policyARN)
		if earlier, taken := attachedAs[name]; taken {
			if earlier == policyARN {
				continue
			}
			return nil, nil, refusal.Refuse(refusal.CodeInvalid,
				"the role %s attaches both %s and %s, and ocel names an attachment after its policy's name, so the two collide; attach one of them",
				r.Name.Pulumi, earlier, policyARN)
		}
		attachedAs[name] = policyARN
		attached, err := AttachManagedPolicy(ctx, r.Name, role, policyARN)
		if err != nil {
			return nil, nil, err
		}
		granted = append(granted, attached)
	}
	for _, policy := range slices.Sorted(maps.Keys(r.Policies)) {
		created, err := NewRolePolicy(ctx, r.Name, role.Name, policy, r.Policies[policy])
		if err != nil {
			return nil, nil, err
		}
		granted = append(granted, created)
	}
	return role, granted, nil
}

func NewRolePolicy(ctx *pulumi.Context, roleName Name, role pulumi.StringInput, policy string, document pulumi.StringInput, opts ...pulumi.ResourceOption) (*awsiam.RolePolicy, error) {
	return awsiam.NewRolePolicy(ctx, roleName.Part(policy), &awsiam.RolePolicyArgs{
		Name:   pulumi.String(policy),
		Role:   role,
		Policy: document,
	}, opts...)
}

func AttachManagedPolicy(ctx *pulumi.Context, roleName Name, role *awsiam.Role, policyARN string) (*awsiam.RolePolicyAttachment, error) {
	return awsiam.NewRolePolicyAttachment(ctx, roleName.Attachment(policyARN), &awsiam.RolePolicyAttachmentArgs{
		Role:      role.Name,
		PolicyArn: pulumi.String(policyARN),
	})
}
