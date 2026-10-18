package iam

import "encoding/json"

const (
	PolicyVersion = "2012-10-17"

	AnyResource = "*"

	LambdaPrincipal    = "lambda.amazonaws.com"
	SchedulerPrincipal = "scheduler.amazonaws.com"
	ECSTasksPrincipal  = "ecs-tasks.amazonaws.com"

	LambdaBasicExecutionPolicyARN = "arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole"
	LambdaVPCAccessPolicyARN      = "arn:aws:iam::aws:policy/service-role/AWSLambdaVPCAccessExecutionRole"
	ECSTaskExecutionPolicyARN     = "arn:aws:iam::aws:policy/service-role/AmazonECSTaskExecutionRolePolicy"
)

type Statement struct {
	Actions   []string
	Resources []string
	Condition map[string]any
}

func Statements(statements []Statement) []map[string]any {
	out := make([]map[string]any, 0, len(statements))
	for _, statement := range statements {
		rendered := map[string]any{
			"Effect":   "Allow",
			"Action":   oneOrMany(statement.Actions),
			"Resource": oneOrMany(statement.Resources),
		}
		if len(statement.Condition) > 0 {
			rendered["Condition"] = statement.Condition
		}
		out = append(out, rendered)
	}
	return out
}

func Document(statements []Statement) map[string]any {
	return map[string]any{"Version": PolicyVersion, "Statement": Statements(statements)}
}

func oneOrMany(values []string) any {
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

func AssumeRolePolicy(servicePrincipal string) string {
	doc := map[string]any{
		"Version": PolicyVersion,
		"Statement": []map[string]any{{
			"Effect":    "Allow",
			"Action":    "sts:AssumeRole",
			"Principal": map[string]any{"Service": servicePrincipal},
		}},
	}
	b, _ := json.Marshal(doc)
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
