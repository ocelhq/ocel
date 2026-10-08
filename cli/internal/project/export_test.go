package project

import "context"

var YAMLToJSON = yamlToJSON

func EvaluateTypeScript(ctx context.Context, path string) ([]byte, error) {
	return evaluateTypeScript(ctx, path, environment{})
}
