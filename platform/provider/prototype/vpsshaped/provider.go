package vpsshaped

import (
	"context"
	"fmt"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
	provider "github.com/ocelhq/ocel/platform/provider/contract"
	"github.com/ocelhq/ocel/platform/provider/prototype/fake"
)

const Kind provider.Kind = "vps"

type Provider struct {
	Host      string
	substrate *fake.Substrate
	records   *fake.Records
	vars      *fake.Vars
	box       *box
}

func New(host string) *Provider {
	return &Provider{Host: host, substrate: &fake.Substrate{Name: "ssh:" + host + " docker+caddy"}, records: &fake.Records{}, vars: &fake.Vars{}, box: &box{host: host}}
}

func (p *Provider) Facts() provider.Facts {
	return provider.Facts{
		Kind:        Kind,
		Serves:      []provider.ResourceKind{provider.ResourcePostgres, provider.ResourceBucket},
		Membrane:    []provider.ResourceKind{provider.ResourceBucket},
		DefaultEdge: "caddy",
	}
}

func (p *Provider) Substrate() provider.Substrate { return p.substrate }
func (p *Provider) Records() provider.RecordStore { return p.records }
func (p *Provider) Vars() provider.VarStore       { return p.vars }
func (p *Provider) DNS() provider.DNSRegistry     { return fake.DNS{} }
func (p *Provider) Deployer() provider.Deployer   { return p.box }

func (p *Provider) Edges() provider.EdgeRegistry {
	return fake.Edges{Default: "caddy", Known: map[edge.Kind]edge.Edge{
		"caddy": fake.Edge{Name: "caddy", Front: p.Host},
	}}
}

type box struct {
	host     string
	services map[string][]string
}

type own struct {
	Spec     provider.Spec     `json:"spec"`
	Compose  string            `json:"compose"`
	Services []string          `json:"services"`
	URLs     map[string]string `json:"urls"`
}

func (b *box) Plan(_ context.Context, spec provider.Spec, prior provider.State) (provider.Plan, error) {
	var was own
	_ = prior.Adapter.Into(&was)
	plan := provider.Plan{}
	for _, app := range spec.Apps {
		action := "create"
		for _, s := range was.Services {
			if s == app.Name {
				action = "restart"
			}
		}
		plan.Changes = append(plan.Changes, provider.Change{Subject: "service " + app.Name, Action: action, Reason: "one container per app; the framework runtime serves it"})
	}
	for _, r := range spec.Resources {
		plan.Changes = append(plan.Changes, provider.Change{Subject: fmt.Sprintf("%s %s", r.Kind, r.Name), Action: "ensure", Reason: "compose service on the box"})
	}
	return plan, nil
}

func (b *box) Upload(_ context.Context, spec provider.Spec, _ provider.Plan, progress provider.Progress) (provider.Uploaded, error) {
	for _, app := range spec.Apps {
		progress.Say("upload", fmt.Sprintf("rsync %s → %s:/srv/ocel/%s/%s", app.Build.Root, b.host, spec.Slug, app.Name))
	}
	return provider.Uploaded{}, nil
}

func (b *box) Apply(_ context.Context, spec provider.Spec, _ provider.Plan, _ provider.Uploaded, _ provider.State, progress provider.Progress) (provider.Deployment, error) {
	state := own{Spec: spec, Compose: fmt.Sprintf("/srv/ocel/%s/compose.yaml", spec.Slug), URLs: map[string]string{}}
	for _, r := range spec.Resources {
		progress.Say("provision", fmt.Sprintf("compose up %s (%s)", r.Name, map[provider.ResourceKind]string{provider.ResourcePostgres: "postgres:17", provider.ResourceBucket: "minio"}[r.Kind]))
	}
	for _, app := range spec.Apps {
		progress.Say("provision", fmt.Sprintf("compose up %s (framework server, port 3000)", app.Name))
		state.Services = append(state.Services, app.Name)
		state.URLs[app.Name] = fmt.Sprintf("http://127.0.0.1:3000/%s", app.Name)
	}
	return &deployment{spec: spec, own: state}, nil
}

func (b *box) Open(st provider.State) (provider.Deployment, error) {
	var o own
	if err := st.Adapter.Into(&o); err != nil {
		return nil, err
	}
	return &deployment{spec: o.Spec, own: o, state: st}, nil
}

type deployment struct {
	spec  provider.Spec
	own   own
	state provider.State
}

func (d *deployment) State() provider.State {
	return provider.State{Slug: d.spec.Slug, Class: d.spec.Class, Adapter: provider.Own(d.own)}
}

func (d *deployment) Resolver() edge.Resolver { return resolver(d.own.URLs) }

func (d *deployment) Links() []provider.Link {
	var out []provider.Link
	for _, r := range d.spec.Resources {
		switch r.Kind {
		case provider.ResourcePostgres:
			out = append(out, provider.Link{Resource: r.Name, Kind: r.Kind, Secrets: map[string]string{"DATABASE_URL": "postgres://ocel@" + r.Name + ":5432/" + r.Name}})
		case provider.ResourceBucket:
			out = append(out, provider.Link{Resource: r.Name, Kind: r.Kind, Values: map[string]string{"BUCKET": r.Name, "S3_ENDPOINT": "http://minio:9000"}})
		}
	}
	return out
}

func (d *deployment) Records() []edge.DeploymentRecord {
	var out []edge.DeploymentRecord
	for _, app := range d.spec.Apps {
		out = append(out, edge.DeploymentRecord{App: app.Name, Framework: app.Framework, DeploymentID: app.DeploymentID, FunctionURLs: map[string]string{"*": d.own.URLs[app.Name]}})
	}
	return out
}

func (d *deployment) Destroy(_ context.Context, p provider.Progress) error {
	p.Say("destroy", "compose down "+d.own.Compose)
	return nil
}

type resolver map[string]string

func (r resolver) FunctionURL(route string) (string, error) {
	for _, v := range r {
		return v, nil
	}
	return "", fmt.Errorf("no service for %q", route)
}
func (r resolver) EdgeCredentials() (edge.Credentials, bool) { return edge.Credentials{}, false }
