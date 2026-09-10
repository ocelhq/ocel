package transform

import "context"

const Provider = "aws"

type Patches map[string]map[string]any

type Resource struct {
	Type string `json:"type"`
	Name string `json:"name"`
	App  string `json:"app,omitempty"`
}

type Request struct {
	Provider  string     `json:"provider"`
	EnvClass  string     `json:"envClass"`
	Env       string     `json:"env"`
	Resources []Resource `json:"resources"`
}

type Result struct {
	Patches Patches
	Tags    map[string]string
}

type Evaluator interface {
	Evaluate(ctx context.Context, req Request) ([]Result, error)
}
