package gcp

import (
	"context"
	"errors"

	"cloud.google.com/go/iam/apiv1/iampb"
	"cloud.google.com/go/kms/apiv1/kmspb"
	"cloud.google.com/go/storage"
	"google.golang.org/api/cloudresourcemanager/v1"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/iam/v1"
	run "google.golang.org/api/run/v2"
	"google.golang.org/api/secretmanager/v1"
	"google.golang.org/genproto/googleapis/type/expr"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
	"github.com/ocelhq/ocel/platform/gcp/provider/iampolicy"
)

func conditionalVersion(bindings []iampolicy.Binding, at int64) int64 {
	if iampolicy.IsConditioned(bindings) {
		return iampolicy.ConditionalVersion
	}
	return at
}

func absentWhenAnswered(err error) error {
	if absent(err) {
		return iampolicy.Absent(err)
	}
	return err
}

func absentWhenNotFound(err error) error {
	if status.Code(err) == codes.NotFound {
		return iampolicy.Absent(err)
	}
	return err
}

func (c *clients) projectPolicy() iampolicy.Resource {
	return iampolicy.Resource{Name: "project " + c.project, Read: func(ctx context.Context) ([]iampolicy.Binding, iampolicy.Write, error) {
		service, err := c.Projects()
		if err != nil {
			return nil, nil, err
		}
		policy, err := googleretry.Attempted(ctx, service.Projects.GetIamPolicy(c.project,
			&cloudresourcemanager.GetIamPolicyRequest{
				Options: &cloudresourcemanager.GetPolicyOptions{RequestedPolicyVersion: iampolicy.ConditionalVersion},
			}).Context(ctx).Do)
		if err != nil {
			return nil, nil, err
		}
		write := func(ctx context.Context, bindings []iampolicy.Binding) error {
			policy.Bindings = make([]*cloudresourcemanager.Binding, 0, len(bindings))
			for _, binding := range bindings {
				policy.Bindings = append(policy.Bindings, &cloudresourcemanager.Binding{Role: binding.Role, Members: binding.Members, Condition: projectExpr(binding.Condition)})
			}
			policy.Version = iampolicy.ConditionalVersion
			_, err := googleretry.Attempted(ctx, service.Projects.SetIamPolicy(c.project,
				&cloudresourcemanager.SetIamPolicyRequest{Policy: policy}).Context(ctx).Do)
			return err
		}
		bindings := make([]iampolicy.Binding, 0, len(policy.Bindings))
		for _, binding := range policy.Bindings {
			bindings = append(bindings, iampolicy.Binding{Role: binding.Role, Members: binding.Members, Condition: projectCondition(binding.Condition)})
		}
		return bindings, write, nil
	}}
}

func projectCondition(condition *cloudresourcemanager.Expr) *iampolicy.Condition {
	if condition == nil {
		return nil
	}
	return &iampolicy.Condition{Title: condition.Title, Description: condition.Description, Expression: condition.Expression}
}

func projectExpr(condition *iampolicy.Condition) *cloudresourcemanager.Expr {
	if condition == nil {
		return nil
	}
	return &cloudresourcemanager.Expr{Title: condition.Title, Description: condition.Description, Expression: condition.Expression}
}

func (c *clients) accountPolicy(account string) iampolicy.Resource {
	path := accountPath(c, account)
	return iampolicy.Resource{Name: "the " + account + " service account", Read: func(ctx context.Context) ([]iampolicy.Binding, iampolicy.Write, error) {
		service, err := c.Accounts()
		if err != nil {
			return nil, nil, err
		}
		policy, err := googleretry.Attempted(ctx, service.Projects.ServiceAccounts.GetIamPolicy(path).Context(ctx).Do)
		if err != nil {
			return nil, nil, absentWhenAnswered(err)
		}
		write := func(ctx context.Context, bindings []iampolicy.Binding) error {
			policy.Bindings = make([]*iam.Binding, 0, len(bindings))
			for _, binding := range bindings {
				policy.Bindings = append(policy.Bindings, &iam.Binding{Role: binding.Role, Members: binding.Members, Condition: accountExpr(binding.Condition)})
			}
			policy.Version = conditionalVersion(bindings, policy.Version)
			_, err := googleretry.Attempted(ctx, service.Projects.ServiceAccounts.SetIamPolicy(path,
				&iam.SetIamPolicyRequest{Policy: policy}).Context(ctx).Do)
			return err
		}
		bindings := make([]iampolicy.Binding, 0, len(policy.Bindings))
		for _, binding := range policy.Bindings {
			bindings = append(bindings, iampolicy.Binding{Role: binding.Role, Members: binding.Members, Condition: accountCondition(binding.Condition)})
		}
		return bindings, write, nil
	}}
}

func accountCondition(condition *iam.Expr) *iampolicy.Condition {
	if condition == nil {
		return nil
	}
	return &iampolicy.Condition{Title: condition.Title, Description: condition.Description, Expression: condition.Expression}
}

func accountExpr(condition *iampolicy.Condition) *iam.Expr {
	if condition == nil {
		return nil
	}
	return &iam.Expr{Title: condition.Title, Description: condition.Description, Expression: condition.Expression}
}

func (c *clients) secretPolicy(name string) iampolicy.Resource {
	path := secretPath(c.project, name)
	return iampolicy.Resource{Name: "the " + name + " secret", Read: func(ctx context.Context) ([]iampolicy.Binding, iampolicy.Write, error) {
		service, err := c.Secrets()
		if err != nil {
			return nil, nil, err
		}
		policy, err := googleretry.Attempted(ctx, service.Projects.Secrets.GetIamPolicy(path).Context(ctx).Do)
		if err != nil {
			return nil, nil, absentWhenAnswered(err)
		}
		write := func(ctx context.Context, bindings []iampolicy.Binding) error {
			policy.Bindings = make([]*secretmanager.Binding, 0, len(bindings))
			for _, binding := range bindings {
				policy.Bindings = append(policy.Bindings, &secretmanager.Binding{Role: binding.Role, Members: binding.Members, Condition: secretExpr(binding.Condition)})
			}
			policy.Version = conditionalVersion(bindings, policy.Version)
			_, err := googleretry.Attempted(ctx, service.Projects.Secrets.SetIamPolicy(path,
				&secretmanager.SetIamPolicyRequest{Policy: policy}).Context(ctx).Do)
			return err
		}
		bindings := make([]iampolicy.Binding, 0, len(policy.Bindings))
		for _, binding := range policy.Bindings {
			bindings = append(bindings, iampolicy.Binding{Role: binding.Role, Members: binding.Members, Condition: secretCondition(binding.Condition)})
		}
		return bindings, write, nil
	}}
}

func secretCondition(condition *secretmanager.Expr) *iampolicy.Condition {
	if condition == nil {
		return nil
	}
	return &iampolicy.Condition{Title: condition.Title, Description: condition.Description, Expression: condition.Expression}
}

func secretExpr(condition *iampolicy.Condition) *secretmanager.Expr {
	if condition == nil {
		return nil
	}
	return &secretmanager.Expr{Title: condition.Title, Description: condition.Description, Expression: condition.Expression}
}

func (c *clients) runServicePolicy(service string) iampolicy.Resource {
	path := c.servicePath(service)
	return iampolicy.Resource{Name: "the Cloud Run service " + service, Read: func(ctx context.Context) ([]iampolicy.Binding, iampolicy.Write, error) {
		services, err := c.Run()
		if err != nil {
			return nil, nil, err
		}
		policy, err := googleretry.Attempted(ctx, func(call ...googleapi.CallOption) (*run.GoogleIamV1Policy, error) {
			return services.Projects.Locations.Services.GetIamPolicy(path).
				OptionsRequestedPolicyVersion(iampolicy.ConditionalVersion).Context(ctx).Do(call...)
		})
		if err != nil {
			return nil, nil, absentWhenAnswered(err)
		}
		write := func(ctx context.Context, bindings []iampolicy.Binding) error {
			policy.Bindings = make([]*run.GoogleIamV1Binding, 0, len(bindings))
			for _, binding := range bindings {
				policy.Bindings = append(policy.Bindings, &run.GoogleIamV1Binding{Role: binding.Role, Members: binding.Members, Condition: runExpr(binding.Condition)})
			}
			policy.Version = iampolicy.ConditionalVersion
			_, err := googleretry.Attempted(ctx, func(call ...googleapi.CallOption) (*run.GoogleIamV1Policy, error) {
				return services.Projects.Locations.Services.SetIamPolicy(path, &run.GoogleIamV1SetIamPolicyRequest{Policy: policy}).Context(ctx).Do(call...)
			})
			return err
		}
		bindings := make([]iampolicy.Binding, 0, len(policy.Bindings))
		for _, binding := range policy.Bindings {
			bindings = append(bindings, iampolicy.Binding{Role: binding.Role, Members: binding.Members, Condition: runCondition(binding.Condition)})
		}
		return bindings, write, nil
	}}
}

func runCondition(condition *run.GoogleTypeExpr) *iampolicy.Condition {
	if condition == nil {
		return nil
	}
	return &iampolicy.Condition{Title: condition.Title, Description: condition.Description, Expression: condition.Expression}
}

func runExpr(condition *iampolicy.Condition) *run.GoogleTypeExpr {
	if condition == nil {
		return nil
	}
	return &run.GoogleTypeExpr{Title: condition.Title, Description: condition.Description, Expression: condition.Expression}
}

func fromProtoBindings(held []*iampb.Binding) []iampolicy.Binding {
	bindings := make([]iampolicy.Binding, 0, len(held))
	for _, binding := range held {
		var condition *iampolicy.Condition
		if held := binding.GetCondition(); held != nil {
			condition = &iampolicy.Condition{Title: held.GetTitle(), Description: held.GetDescription(), Expression: held.GetExpression()}
		}
		bindings = append(bindings, iampolicy.Binding{Role: binding.GetRole(), Members: binding.GetMembers(), Condition: condition})
	}
	return bindings
}

func toProtoBindings(bindings []iampolicy.Binding) []*iampb.Binding {
	held := make([]*iampb.Binding, 0, len(bindings))
	for _, binding := range bindings {
		var condition *expr.Expr
		if binding.Condition != nil {
			condition = &expr.Expr{Title: binding.Condition.Title, Description: binding.Condition.Description, Expression: binding.Condition.Expression}
		}
		held = append(held, &iampb.Binding{Role: binding.Role, Members: binding.Members, Condition: condition})
	}
	return held
}

func (c *clients) keyPolicy(key, named string) iampolicy.Resource {
	return iampolicy.Resource{Name: "the " + named + " key", Read: func(ctx context.Context) ([]iampolicy.Binding, iampolicy.Write, error) {
		client, err := c.KMS()
		if err != nil {
			return nil, nil, err
		}
		if _, err := googleretry.Called(ctx, func() (*kmspb.CryptoKey, error) {
			return client.GetCryptoKey(ctx, &kmspb.GetCryptoKeyRequest{Name: key})
		}); err != nil {
			return nil, nil, absentWhenNotFound(err)
		}
		policy, err := googleretry.Called(ctx, func() (*iampb.Policy, error) {
			return client.GetIamPolicy(ctx, &iampb.GetIamPolicyRequest{Resource: key})
		})
		if err != nil {
			return nil, nil, absentWhenNotFound(err)
		}
		write := func(ctx context.Context, bindings []iampolicy.Binding) error {
			policy.Bindings = toProtoBindings(bindings)
			policy.Version = int32(conditionalVersion(bindings, int64(policy.GetVersion())))
			_, err := googleretry.Called(ctx, func() (*iampb.Policy, error) {
				return client.SetIamPolicy(ctx, &iampb.SetIamPolicyRequest{Resource: key, Policy: policy})
			})
			return err
		}
		return fromProtoBindings(policy.GetBindings()), write, nil
	}}
}

func (c *clients) queuePolicy(queue, named string) iampolicy.Resource {
	return iampolicy.Resource{Name: named, Read: func(ctx context.Context) ([]iampolicy.Binding, iampolicy.Write, error) {
		client, err := c.Workload().CloudTasks()
		if err != nil {
			return nil, nil, err
		}
		policy, err := googleretry.Called(ctx, func() (*iampb.Policy, error) {
			return client.GetIamPolicy(ctx, &iampb.GetIamPolicyRequest{Resource: queue})
		})
		if err != nil {
			return nil, nil, absentWhenNotFound(err)
		}
		write := func(ctx context.Context, bindings []iampolicy.Binding) error {
			policy.Bindings = toProtoBindings(bindings)
			policy.Version = int32(conditionalVersion(bindings, int64(policy.GetVersion())))
			_, err := googleretry.Called(ctx, func() (*iampb.Policy, error) {
				return client.SetIamPolicy(ctx, &iampb.SetIamPolicyRequest{Resource: queue, Policy: policy})
			})
			return err
		}
		return fromProtoBindings(policy.GetBindings()), write, nil
	}}
}

func (c *clients) bucketPolicy(bucket string) iampolicy.Resource {
	return iampolicy.Resource{Name: "the Cloud Storage bucket " + bucket, Read: func(ctx context.Context) ([]iampolicy.Binding, iampolicy.Write, error) {
		client, err := c.Storage()
		if err != nil {
			return nil, nil, err
		}
		handle := client.Bucket(bucket).IAM().V3()
		policy, err := handle.Policy(ctx)
		if errors.Is(err, storage.ErrBucketNotExist) {
			return nil, nil, iampolicy.Absent(err)
		}
		if err != nil {
			return nil, nil, absentWhenAnswered(err)
		}
		write := func(ctx context.Context, bindings []iampolicy.Binding) error {
			policy.Bindings = toProtoBindings(bindings)
			return handle.SetPolicy(ctx, policy)
		}
		return fromProtoBindings(policy.Bindings), write, nil
	}}
}
