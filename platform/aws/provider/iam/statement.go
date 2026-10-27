package iam

import "encoding/json"

const (
	PolicyVersion = "2012-10-17"

	AnyResource = "*"

	LambdaPrincipal     = "lambda.amazonaws.com"
	SchedulerPrincipal  = "scheduler.amazonaws.com"
	ECSTasksPrincipal   = "ecs-tasks.amazonaws.com"
	APIGatewayPrincipal = "apigateway.amazonaws.com"
	CloudFrontPrincipal = "cloudfront.amazonaws.com"

	LambdaBasicExecutionPolicyARN = "arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole"
	LambdaVPCAccessPolicyARN      = "arn:aws:iam::aws:policy/service-role/AWSLambdaVPCAccessExecutionRole"
	ECSTaskExecutionPolicyARN     = "arn:aws:iam::aws:policy/service-role/AmazonECSTaskExecutionRolePolicy"
)

type Statement struct {
	Sid            string
	Principal      Principal
	Actions        []string
	Resources      []string
	ResourceValues []any
	Condition      map[string]any
}

type Principal struct {
	Service   string
	AWS       any
	Federated any
}

func (p Principal) rendered() map[string]any {
	out := map[string]any{}
	if p.Service != "" {
		out["Service"] = p.Service
	}
	if p.AWS != nil {
		out["AWS"] = p.AWS
	}
	if p.Federated != nil {
		out["Federated"] = p.Federated
	}
	return out
}

func Statements(statements []Statement) []map[string]any {
	out := make([]map[string]any, 0, len(statements))
	for _, statement := range statements {
		rendered := map[string]any{
			"Effect": "Allow",
			"Action": oneOrMany(statement.Actions),
		}
		if resources := resourcesOf(statement); len(resources) > 0 {
			rendered["Resource"] = oneOrMany(resources)
		}
		if statement.Sid != "" {
			rendered["Sid"] = statement.Sid
		}
		if principal := statement.Principal.rendered(); len(principal) > 0 {
			rendered["Principal"] = principal
		}
		if len(statement.Condition) > 0 {
			rendered["Condition"] = statement.Condition
		}
		out = append(out, rendered)
	}
	return out
}

func resourcesOf(statement Statement) []any {
	out := make([]any, 0, len(statement.Resources)+len(statement.ResourceValues))
	for _, resource := range statement.Resources {
		out = append(out, resource)
	}
	return append(out, statement.ResourceValues...)
}

func Document(statements []Statement) map[string]any {
	return map[string]any{"Version": PolicyVersion, "Statement": Statements(statements)}
}

func oneOrMany[T any](values []T) any {
	if len(values) == 1 {
		return values[0]
	}
	return values
}

func MergeConditions(conditions ...map[string]any) map[string]any {
	merged := map[string]any{}
	for _, condition := range conditions {
		for operator, operands := range condition {
			existing, ok := merged[operator].(map[string]any)
			if !ok {
				merged[operator] = operands
				continue
			}
			for key, value := range operands.(map[string]any) {
				existing[key] = value
			}
		}
	}
	return merged
}

func AssumeRoleDocument(servicePrincipal string) map[string]any {
	return Document([]Statement{{Principal: Principal{Service: servicePrincipal}, Actions: []string{"sts:AssumeRole"}}})
}

func AssumeRolePolicy(servicePrincipal string) string {
	b, _ := json.Marshal(AssumeRoleDocument(servicePrincipal))
	return string(b)
}

func InlinePolicy(actions, resources []string, condition map[string]any) (string, error) {
	statement := map[string]any{
		"Effect":   "Allow",
		"Action":   actions,
		"Resource": resources,
	}
	if len(condition) > 0 {
		statement["Condition"] = condition
	}
	b, err := json.Marshal(map[string]any{"Version": PolicyVersion, "Statement": []map[string]any{statement}})
	if err != nil {
		return "", err
	}
	return string(b), nil
}
