package gcloud

import (
	"context"
	"errors"

	"cloud.google.com/go/iam/apiv1/iampb"
	"cloud.google.com/go/kms/apiv1/kmspb"
	"cloud.google.com/go/storage"
	"google.golang.org/api/artifactregistry/v1"
	"google.golang.org/api/cloudresourcemanager/v1"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/iam/v1"
	run "google.golang.org/api/run/v2"
	"google.golang.org/api/secretmanager/v1"
	"google.golang.org/genproto/googleapis/type/expr"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/policy"
)

func conditionalVersion(bindings []policy.Binding, at int64) int64 {
	if policy.IsConditioned(bindings) {
		return policy.ConditionalVersion
	}
	return at
}

func absentWhenAnswered(err error) error {
	if Absent(err) {
		return policy.Absent(err)
	}
	return err
}

func AbsentWhenNotFound(err error) error {
	if status.Code(err) == codes.NotFound {
		return policy.Absent(err)
	}
	return err
}

func ProjectPolicy(c *Clients) policy.Resource {
	return ProjectPolicyOf(c, c.Project())
}

func ProjectPolicyOf(c *Clients, project string) policy.Resource {
	return policy.Resource{Name: "project " + project, Read: func(ctx context.Context) ([]policy.Binding, policy.Write, error) {
		service, err := c.Projects()
		if err != nil {
			return nil, nil, err
		}
		held, err := googleretry.Attempted(ctx, service.Projects.GetIamPolicy(project,
			&cloudresourcemanager.GetIamPolicyRequest{
				Options: &cloudresourcemanager.GetPolicyOptions{RequestedPolicyVersion: policy.ConditionalVersion},
			}).Context(ctx).Do)
		if err != nil {
			return nil, nil, err
		}
		write := func(ctx context.Context, bindings []policy.Binding) error {
			held.Bindings = make([]*cloudresourcemanager.Binding, 0, len(bindings))
			for _, binding := range bindings {
				held.Bindings = append(held.Bindings, &cloudresourcemanager.Binding{Role: binding.Role, Members: binding.Members, Condition: projectExpr(binding.Condition)})
			}
			held.Version = policy.ConditionalVersion
			_, err := googleretry.Attempted(ctx, service.Projects.SetIamPolicy(project,
				&cloudresourcemanager.SetIamPolicyRequest{Policy: held}).Context(ctx).Do)
			return err
		}
		bindings := make([]policy.Binding, 0, len(held.Bindings))
		for _, binding := range held.Bindings {
			bindings = append(bindings, policy.Binding{Role: binding.Role, Members: binding.Members, Condition: projectCondition(binding.Condition)})
		}
		return bindings, write, nil
	}}
}

func projectCondition(condition *cloudresourcemanager.Expr) *policy.Condition {
	if condition == nil {
		return nil
	}
	return &policy.Condition{Title: condition.Title, Description: condition.Description, Expression: condition.Expression}
}

func projectExpr(condition *policy.Condition) *cloudresourcemanager.Expr {
	if condition == nil {
		return nil
	}
	return &cloudresourcemanager.Expr{Title: condition.Title, Description: condition.Description, Expression: condition.Expression}
}

func AccountPolicy(c *Clients, account string) policy.Resource {
	return accountPolicyAt(c, AccountPath(c, account), "the "+account+" service account")
}

func AccountPolicyAt(c *Clients, path string) policy.Resource {
	return accountPolicyAt(c, path, "the service account "+path)
}

func accountPolicyAt(c *Clients, path, named string) policy.Resource {
	return policy.Resource{Name: named, Read: func(ctx context.Context) ([]policy.Binding, policy.Write, error) {
		service, err := c.Accounts()
		if err != nil {
			return nil, nil, err
		}
		held, err := googleretry.Attempted(ctx, service.Projects.ServiceAccounts.GetIamPolicy(path).Context(ctx).Do)
		if err != nil {
			return nil, nil, absentWhenAnswered(err)
		}
		write := func(ctx context.Context, bindings []policy.Binding) error {
			held.Bindings = make([]*iam.Binding, 0, len(bindings))
			for _, binding := range bindings {
				held.Bindings = append(held.Bindings, &iam.Binding{Role: binding.Role, Members: binding.Members, Condition: accountExpr(binding.Condition)})
			}
			held.Version = conditionalVersion(bindings, held.Version)
			_, err := googleretry.Attempted(ctx, service.Projects.ServiceAccounts.SetIamPolicy(path,
				&iam.SetIamPolicyRequest{Policy: held}).Context(ctx).Do)
			return err
		}
		bindings := make([]policy.Binding, 0, len(held.Bindings))
		for _, binding := range held.Bindings {
			bindings = append(bindings, policy.Binding{Role: binding.Role, Members: binding.Members, Condition: accountCondition(binding.Condition)})
		}
		return bindings, write, nil
	}}
}

func accountCondition(condition *iam.Expr) *policy.Condition {
	if condition == nil {
		return nil
	}
	return &policy.Condition{Title: condition.Title, Description: condition.Description, Expression: condition.Expression}
}

func accountExpr(condition *policy.Condition) *iam.Expr {
	if condition == nil {
		return nil
	}
	return &iam.Expr{Title: condition.Title, Description: condition.Description, Expression: condition.Expression}
}

func SecretPolicy(c *Clients, name string) policy.Resource {
	path := SecretPath(c.Project(), name)
	return policy.Resource{Name: "the " + name + " secret", Read: func(ctx context.Context) ([]policy.Binding, policy.Write, error) {
		service, err := c.Secrets()
		if err != nil {
			return nil, nil, err
		}
		held, err := googleretry.Attempted(ctx, service.Projects.Secrets.GetIamPolicy(path).Context(ctx).Do)
		if err != nil {
			return nil, nil, absentWhenAnswered(err)
		}
		write := func(ctx context.Context, bindings []policy.Binding) error {
			held.Bindings = make([]*secretmanager.Binding, 0, len(bindings))
			for _, binding := range bindings {
				held.Bindings = append(held.Bindings, &secretmanager.Binding{Role: binding.Role, Members: binding.Members, Condition: secretExpr(binding.Condition)})
			}
			held.Version = conditionalVersion(bindings, held.Version)
			_, err := googleretry.Attempted(ctx, service.Projects.Secrets.SetIamPolicy(path,
				&secretmanager.SetIamPolicyRequest{Policy: held}).Context(ctx).Do)
			return err
		}
		bindings := make([]policy.Binding, 0, len(held.Bindings))
		for _, binding := range held.Bindings {
			bindings = append(bindings, policy.Binding{Role: binding.Role, Members: binding.Members, Condition: secretCondition(binding.Condition)})
		}
		return bindings, write, nil
	}}
}

func secretCondition(condition *secretmanager.Expr) *policy.Condition {
	if condition == nil {
		return nil
	}
	return &policy.Condition{Title: condition.Title, Description: condition.Description, Expression: condition.Expression}
}

func secretExpr(condition *policy.Condition) *secretmanager.Expr {
	if condition == nil {
		return nil
	}
	return &secretmanager.Expr{Title: condition.Title, Description: condition.Description, Expression: condition.Expression}
}

func RunServicePolicy(c *Clients, service string) policy.Resource {
	path := c.ServicePath(service)
	return policy.Resource{Name: "the Cloud Run service " + service, Read: func(ctx context.Context) ([]policy.Binding, policy.Write, error) {
		services, err := c.Run()
		if err != nil {
			return nil, nil, err
		}
		held, err := googleretry.Attempted(ctx, func(call ...googleapi.CallOption) (*run.GoogleIamV1Policy, error) {
			return services.Projects.Locations.Services.GetIamPolicy(path).
				OptionsRequestedPolicyVersion(policy.ConditionalVersion).Context(ctx).Do(call...)
		})
		if err != nil {
			return nil, nil, absentWhenAnswered(err)
		}
		write := func(ctx context.Context, bindings []policy.Binding) error {
			held.Bindings = make([]*run.GoogleIamV1Binding, 0, len(bindings))
			for _, binding := range bindings {
				held.Bindings = append(held.Bindings, &run.GoogleIamV1Binding{Role: binding.Role, Members: binding.Members, Condition: runExpr(binding.Condition)})
			}
			held.Version = policy.ConditionalVersion
			_, err := googleretry.Attempted(ctx, func(call ...googleapi.CallOption) (*run.GoogleIamV1Policy, error) {
				return services.Projects.Locations.Services.SetIamPolicy(path, &run.GoogleIamV1SetIamPolicyRequest{Policy: held}).Context(ctx).Do(call...)
			})
			return err
		}
		bindings := make([]policy.Binding, 0, len(held.Bindings))
		for _, binding := range held.Bindings {
			bindings = append(bindings, policy.Binding{Role: binding.Role, Members: binding.Members, Condition: runCondition(binding.Condition)})
		}
		return bindings, write, nil
	}}
}

func runCondition(condition *run.GoogleTypeExpr) *policy.Condition {
	if condition == nil {
		return nil
	}
	return &policy.Condition{Title: condition.Title, Description: condition.Description, Expression: condition.Expression}
}

func runExpr(condition *policy.Condition) *run.GoogleTypeExpr {
	if condition == nil {
		return nil
	}
	return &run.GoogleTypeExpr{Title: condition.Title, Description: condition.Description, Expression: condition.Expression}
}

func RepositoryPolicy(c *Clients, path string) policy.Resource {
	return policy.Resource{Name: "the Artifact Registry repository " + path, Read: func(ctx context.Context) ([]policy.Binding, policy.Write, error) {
		service, err := c.Repositories()
		if err != nil {
			return nil, nil, err
		}
		held, err := googleretry.Attempted(ctx, service.Projects.Locations.Repositories.GetIamPolicy(path).
			OptionsRequestedPolicyVersion(policy.ConditionalVersion).Context(ctx).Do)
		if err != nil {
			return nil, nil, absentWhenAnswered(err)
		}
		write := func(ctx context.Context, bindings []policy.Binding) error {
			held.Bindings = make([]*artifactregistry.Binding, 0, len(bindings))
			for _, binding := range bindings {
				held.Bindings = append(held.Bindings, &artifactregistry.Binding{Role: binding.Role, Members: binding.Members, Condition: repositoryExpr(binding.Condition)})
			}
			held.Version = conditionalVersion(bindings, held.Version)
			_, err := googleretry.Attempted(ctx, service.Projects.Locations.Repositories.SetIamPolicy(path,
				&artifactregistry.SetIamPolicyRequest{Policy: held}).Context(ctx).Do)
			return err
		}
		bindings := make([]policy.Binding, 0, len(held.Bindings))
		for _, binding := range held.Bindings {
			bindings = append(bindings, policy.Binding{Role: binding.Role, Members: binding.Members, Condition: repositoryCondition(binding.Condition)})
		}
		return bindings, write, nil
	}}
}

func repositoryCondition(condition *artifactregistry.Expr) *policy.Condition {
	if condition == nil {
		return nil
	}
	return &policy.Condition{Title: condition.Title, Description: condition.Description, Expression: condition.Expression}
}

func repositoryExpr(condition *policy.Condition) *artifactregistry.Expr {
	if condition == nil {
		return nil
	}
	return &artifactregistry.Expr{Title: condition.Title, Description: condition.Description, Expression: condition.Expression}
}

func FromProtoBindings(held []*iampb.Binding) []policy.Binding {
	bindings := make([]policy.Binding, 0, len(held))
	for _, binding := range held {
		var condition *policy.Condition
		if held := binding.GetCondition(); held != nil {
			condition = &policy.Condition{Title: held.GetTitle(), Description: held.GetDescription(), Expression: held.GetExpression()}
		}
		bindings = append(bindings, policy.Binding{Role: binding.GetRole(), Members: binding.GetMembers(), Condition: condition})
	}
	return bindings
}

func ToProtoBindings(bindings []policy.Binding) []*iampb.Binding {
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

func KeyPolicy(c *Clients, key, named string) policy.Resource {
	return policy.Resource{Name: "the " + named + " key", Read: func(ctx context.Context) ([]policy.Binding, policy.Write, error) {
		client, err := c.KMS()
		if err != nil {
			return nil, nil, err
		}
		if _, err := googleretry.Called(ctx, func() (*kmspb.CryptoKey, error) {
			return client.GetCryptoKey(ctx, &kmspb.GetCryptoKeyRequest{Name: key})
		}); err != nil {
			return nil, nil, AbsentWhenNotFound(err)
		}
		held, err := googleretry.Called(ctx, func() (*iampb.Policy, error) {
			return client.GetIamPolicy(ctx, &iampb.GetIamPolicyRequest{Resource: key})
		})
		if err != nil {
			return nil, nil, AbsentWhenNotFound(err)
		}
		write := func(ctx context.Context, bindings []policy.Binding) error {
			held.Bindings = ToProtoBindings(bindings)
			held.Version = int32(conditionalVersion(bindings, int64(held.GetVersion())))
			_, err := googleretry.Called(ctx, func() (*iampb.Policy, error) {
				return client.SetIamPolicy(ctx, &iampb.SetIamPolicyRequest{Resource: key, Policy: held})
			})
			return err
		}
		return FromProtoBindings(held.GetBindings()), write, nil
	}}
}

func QueuePolicy(c *Clients, queue, named string) policy.Resource {
	return policy.Resource{Name: named, Read: func(ctx context.Context) ([]policy.Binding, policy.Write, error) {
		client, err := c.Workload().CloudTasks()
		if err != nil {
			return nil, nil, err
		}
		held, err := googleretry.Called(ctx, func() (*iampb.Policy, error) {
			return client.GetIamPolicy(ctx, &iampb.GetIamPolicyRequest{Resource: queue})
		})
		if err != nil {
			return nil, nil, AbsentWhenNotFound(err)
		}
		write := func(ctx context.Context, bindings []policy.Binding) error {
			held.Bindings = ToProtoBindings(bindings)
			held.Version = int32(conditionalVersion(bindings, int64(held.GetVersion())))
			_, err := googleretry.Called(ctx, func() (*iampb.Policy, error) {
				return client.SetIamPolicy(ctx, &iampb.SetIamPolicyRequest{Resource: queue, Policy: held})
			})
			return err
		}
		return FromProtoBindings(held.GetBindings()), write, nil
	}}
}

func BucketPolicy(c *Clients, bucket string) policy.Resource {
	return policy.Resource{Name: "the Cloud Storage bucket " + bucket, Read: func(ctx context.Context) ([]policy.Binding, policy.Write, error) {
		client, err := c.Storage()
		if err != nil {
			return nil, nil, err
		}
		handle := client.Bucket(bucket).IAM().V3()
		held, err := handle.Policy(ctx)
		if errors.Is(err, storage.ErrBucketNotExist) {
			return nil, nil, policy.Absent(err)
		}
		if err != nil {
			return nil, nil, absentWhenAnswered(err)
		}
		write := func(ctx context.Context, bindings []policy.Binding) error {
			held.Bindings = ToProtoBindings(bindings)
			return handle.SetPolicy(ctx, held)
		}
		return FromProtoBindings(held.Bindings), write, nil
	}}
}
