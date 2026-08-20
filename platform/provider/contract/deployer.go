package provider

import (
	"context"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type Spec struct {
	Slug        string
	Class       edge.Class
	Environment string
	Ephemeral   bool
	Tag         string
	Options     []byte
	Apps        []App
	Resources   []Resource
	Usages      []Usage
	Edge        edge.Kind
	Needs       []edge.Need
}

type App struct {
	Name         string
	Framework    string
	DeploymentID string
	Folder       string
	Build        Build
	Domains      []string
	Variables    []Variable
}

type Build struct {
	Root   string
	Serve  edge.ServeDescriptor
	Routes []Route
	Assets string
}

type Route struct {
	ID       string
	Entry    string
	Artifact string
	Runtime  string
}

type Resource struct {
	Name   string
	Kind   ResourceKind
	Config []byte
	Linked bool
}

type Usage struct {
	App      string
	Resource string
}

type Variable struct {
	Key     string
	Value   string
	Folder  string
	Version int64
	Secret  bool
}

type Plan struct {
	Ready    bool
	Refusals []Refusal
	Changes  []Change
	Stages   []Stage
	Private  Private
}

type Refusal struct {
	Subject string
	Reason  string
	Fix     string
}

type Change struct {
	Subject string
	Action  string
	Reason  string
}

type Stage struct {
	ID     StageID
	Title  string
	Parent StageID
}

type Uploaded struct {
	Private Private
}

type Deployer interface {
	Plan(ctx context.Context, spec Spec, prior State) (Plan, error)
	Upload(ctx context.Context, spec Spec, plan Plan, progress Progress) (Uploaded, error)
	Apply(ctx context.Context, spec Spec, plan Plan, up Uploaded, prior State, progress Progress) (Deployment, error)
	Open(state State) (Deployment, error)
}

type Deployment interface {
	State() State
	Resolver() edge.Resolver
	Links() []Link
	Records() []edge.DeploymentRecord
	Destroy(ctx context.Context, progress Progress) error
}

type State struct {
	Slug    string     `json:"slug"`
	Class   edge.Class `json:"class"`
	Adapter Private    `json:"adapter,omitzero"`
}

type Link struct {
	Resource string
	Kind     ResourceKind
	Values   map[string]string
	Secrets  map[string]string
}
