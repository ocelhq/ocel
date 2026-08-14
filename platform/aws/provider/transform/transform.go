package transform

import "context"

type Surfaces map[string]map[string]any

type Resource struct {
	Type     string   `json:"type"`
	Name     string   `json:"name"`
	App      string   `json:"app,omitempty"`
	Surfaces Surfaces `json:"surfaces"`
}

type Request struct {
	EnvClass  string     `json:"envClass"`
	Env       string     `json:"env"`
	Resources []Resource `json:"resources"`
}

type Evaluator interface {
	Evaluate(ctx context.Context, req Request) ([]Surfaces, error)
}
