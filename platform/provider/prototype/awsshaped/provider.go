package awsshaped

import (
	"context"
	"fmt"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
	provider "github.com/ocelhq/ocel/platform/provider/contract"
	"github.com/ocelhq/ocel/platform/provider/kit/pulumi"
	"github.com/ocelhq/ocel/platform/provider/prototype/fake"
)

const Kind provider.Kind = "aws"

type Provider struct {
	Region    string
	substrate *fake.Substrate
	records   *fake.Records
	vars      *fake.Vars
	engine    *engine
}

func New(region string) *Provider {
	return &Provider{Region: region, substrate: &fake.Substrate{Name: "cloudformation:ocel-bootstrap"}, records: &fake.Records{}, vars: &fake.Vars{}, engine: &engine{}}
}

func (p *Provider) Facts() provider.Facts {
	return provider.Facts{
		Kind:        Kind,
		Serves:      []provider.ResourceKind{provider.ResourcePostgres, provider.ResourceBucket},
		Membrane:    []provider.ResourceKind{provider.ResourceBucket},
		Features:    []string{"isr", "image-optimization", "cloudflare-edge"},
		DefaultEdge: "cloudfront",
	}
}

func (p *Provider) Substrate() provider.Substrate { return p.substrate }
func (p *Provider) Records() provider.RecordStore { return p.records }
func (p *Provider) Vars() provider.VarStore       { return p.vars }
func (p *Provider) DNS() provider.DNSRegistry     { return fake.DNS{} }

func (p *Provider) Edges() provider.EdgeRegistry {
	return fake.Edges{Default: "cloudfront", Known: map[edge.Kind]edge.Edge{
		"cloudfront":  fake.Edge{Name: "cloudfront", Front: "cloudfront.net"},
		"api-gateway": fake.Edge{Name: "api-gateway", Front: "execute-api.amazonaws.com"},
		"cloudflare":  fake.Edge{Name: "cloudflare", Runs: true, Front: "workers.dev"},
	}}
}

func (p *Provider) Certificates() provider.Certificates { return acm{} }

func (p *Provider) Deployer() provider.Deployer {
	return pulumi.Deployer{
		Engine:    p.engine,
		Program:   program,
		StackName: func(spec provider.Spec) string { return fmt.Sprintf("ocel-%s-%s", spec.Slug, spec.Class) },
		Place: func(_ context.Context, spec provider.Spec, progress provider.Progress) error {
			for i, app := range spec.Apps {
				progress.Say("upload", fmt.Sprintf("s3: %s/%s → artifacts bucket", app.Name, app.Build.Root))
				progress.Count("upload", uint32(i+1), uint32(len(spec.Apps)))
			}
			return nil
		},
		Read: read,
	}
}

func program(_ context.Context, spec provider.Spec, export func(key, value string)) error {
	for _, r := range spec.Resources {
		switch r.Kind {
		case provider.ResourcePostgres:
			export("postgres:"+r.Name, fmt.Sprintf("aurora-serverless-v2://%s-%s", spec.Slug, r.Name))
		case provider.ResourceBucket:
			export("bucket:"+r.Name, fmt.Sprintf("s3://%s-%s", spec.Slug, r.Name))
		}
	}
	for _, app := range spec.Apps {
		for _, route := range app.Build.Routes {
			export("function:"+app.Name+"/"+route.ID, fmt.Sprintf("https://%s-%s.lambda-url.%s.on.aws", app.Name, route.ID, "eu-west-1"))
		}
	}
	return nil
}

func read(spec provider.Spec, outputs pulumi.Outputs) pulumi.Realized {
	var r pulumi.Realized
	urls := map[string]string{}
	for k, v := range outputs {
		switch {
		case len(k) > 9 && k[:9] == "postgres:":
			r.Links = append(r.Links, provider.Link{Resource: k[9:], Kind: provider.ResourcePostgres, Secrets: map[string]string{"DATABASE_URL": v}})
		case len(k) > 7 && k[:7] == "bucket:":
			r.Links = append(r.Links, provider.Link{Resource: k[7:], Kind: provider.ResourceBucket, Values: map[string]string{"BUCKET": v}})
		case len(k) > 9 && k[:9] == "function:":
			urls[k[9:]] = v
		}
	}
	for _, app := range spec.Apps {
		r.Records = append(r.Records, edge.DeploymentRecord{App: app.Name, Framework: app.Framework, DeploymentID: app.DeploymentID, FunctionURLs: urls})
	}
	r.Resolver = resolver(urls)
	return r
}

type resolver map[string]string

func (r resolver) FunctionURL(route string) (string, error) {
	for k, v := range r {
		if len(k) >= len(route) && k[len(k)-len(route):] == route {
			return v, nil
		}
	}
	return "", fmt.Errorf("no function for route %q", route)
}
func (r resolver) EdgeCredentials() (edge.Credentials, bool) { return edge.Credentials{}, false }

type engine struct{ stacks map[string]pulumi.Outputs }

func (e *engine) Preview(_ context.Context, stack string, prog pulumi.Program, spec provider.Spec) ([]provider.Change, error) {
	var changes []provider.Change
	_ = prog(context.Background(), spec, func(k, _ string) {
		action := "create"
		if _, ok := e.stacks[stack][k]; ok {
			action = "update"
		}
		changes = append(changes, provider.Change{Subject: k, Action: action})
	})
	return changes, nil
}

func (e *engine) Up(_ context.Context, stack string, prog pulumi.Program, spec provider.Spec, say func(string)) (pulumi.Outputs, error) {
	if e.stacks == nil {
		e.stacks = map[string]pulumi.Outputs{}
	}
	out := pulumi.Outputs{}
	err := prog(context.Background(), spec, func(k, v string) { out[k] = v; say("pulumi: + " + k) })
	e.stacks[stack] = out
	return out, err
}

func (e *engine) Outputs(_ context.Context, stack string) (pulumi.Outputs, error) {
	return e.stacks[stack], nil
}

func (e *engine) Destroy(_ context.Context, stack string, say func(string)) error {
	for k := range e.stacks[stack] {
		say("pulumi: - " + k)
	}
	delete(e.stacks, stack)
	return nil
}

type acm struct{}

func (acm) Issue(_ context.Context, hostnames []string) (provider.Certificate, error) {
	return provider.Certificate{ID: "arn:aws:acm:cert/" + hostnames[0], Hostnames: hostnames, Issued: true}, nil
}
func (acm) Describe(_ context.Context, id string) (provider.Certificate, error) {
	return provider.Certificate{ID: id, Issued: true}, nil
}
func (acm) Discard(context.Context, string) error       { return nil }
func (acm) Probe(context.Context, string) (bool, error) { return true, nil }
