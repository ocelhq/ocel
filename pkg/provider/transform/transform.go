package transform

import "context"

type Patches map[string]map[string]any

type Resource struct {
	Type string `json:"type"`
	Name string `json:"name"`
	App  string `json:"app,omitempty"`
}

type Request struct {
	Provider  string     `json:"provider"`
	EnvTier   string     `json:"envTier"`
	Env       string     `json:"env"`
	Resources []Resource `json:"resources"`
}

type Result struct {
	Patches Patches
	Tags    map[string]string
}

type Pass interface {
	Evaluate(ctx context.Context, req Request) ([]Result, error)
}
