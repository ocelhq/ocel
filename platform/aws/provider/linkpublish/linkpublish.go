package linkpublish

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/kms"

	linksv1 "github.com/ocelhq/ocel/pkg/proto/links/v1"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	"github.com/ocelhq/ocel/platform/aws/provider/sdkconfig"
	"github.com/ocelhq/ocel/platform/aws/provider/vars"
)

var ErrNoSubstrate = errors.New("linkpublish: no ocel substrate")

type Request struct {
	Project     string            `json:"project"`
	Publisher   string            `json:"publisher"`
	Class       string            `json:"class"`
	Environment string            `json:"environment,omitempty"`
	Region      string            `json:"region,omitempty"`
	Records     []json.RawMessage `json:"records,omitempty"`
}

type Response struct {
	Published []string `json:"published"`
	Pruned    int      `json:"pruned"`
	Table     string   `json:"table"`
}

func (r Request) validate() error {
	if r.Project == "" {
		return fmt.Errorf("a project is required: it is the ocel project whose apps consume these links")
	}
	if r.Publisher == "" {
		return fmt.Errorf("a publisher is required: it is what makes the records this stack wrote the ones it may prune")
	}
	if r.Class != bootstrap.ClassProduction && r.Class != bootstrap.ClassPreview {
		return fmt.Errorf("class %q is neither %q nor %q; a publisher targets an ocel coordinate, never a stage or stack name",
			r.Class, bootstrap.ClassProduction, bootstrap.ClassPreview)
	}
	if r.Environment == vars.ClassWideEnvironment {
		return fmt.Errorf("%q is reserved: leave the environment off to publish to the whole class, including every ephemeral preview", vars.ClassWideEnvironment)
	}
	if r.Environment != "" && r.Class != bootstrap.ClassPreview {
		return fmt.Errorf("environment %q is named alongside class %q: an ocel coordinate is a class and, in %s, one preview environment; leave the environment off",
			r.Environment, r.Class, bootstrap.ClassPreview)
	}
	return nil
}

func (r Request) records() ([]*linksv1.Link, error) {
	out := make([]*linksv1.Link, 0, len(r.Records))
	for i, raw := range r.Records {
		link, err := vars.DecodeLink(raw)
		if err != nil {
			return nil, fmt.Errorf("record %d is not a links.v1.Link: %w", i, err)
		}
		if err := vars.Verify(link); err != nil {
			return nil, err
		}
		out = append(out, link)
	}
	return out, nil
}

func Substrate(ctx context.Context, cfn bootstrap.CFNDescriber, class string) (bootstrap.Deployed, error) {
	check := bootstrap.CheckDeployed
	if class == bootstrap.ClassPreview {
		check = bootstrap.CheckDeployedPreview
	}
	deployed, err := check(ctx, cfn)
	if err != nil {
		return bootstrap.Deployed{}, err
	}
	if !deployed.Present {
		return bootstrap.Deployed{}, fmt.Errorf(
			"this AWS account holds no ocel %s substrate, so the links this stack publishes have nowhere to land and every app consuming them would fail at cold start instead. "+
				"Run `ocel bootstrap%s` against this account, then apply again: %w",
			class, previewFlag(class), ErrNoSubstrate)
	}
	if deployed.VarsTable == "" || deployed.VarsKeyARN == "" {
		return bootstrap.Deployed{}, fmt.Errorf(
			"the ocel %s substrate is present but carries no variable store (a partial rollback?), so a published link has nowhere to land. "+
				"Re-run `ocel bootstrap%s` against this account: %w",
			class, previewFlag(class), ErrNoSubstrate)
	}
	return deployed, nil
}

func previewFlag(class string) string {
	if class == bootstrap.ClassPreview {
		return " --preview"
	}
	return ""
}

type Clients struct {
	CFN    bootstrap.CFNDescriber
	Dynamo vars.DynamoAPI
	KMS    vars.CryptoAPI
}

func Load(ctx context.Context, region string) (Clients, error) {
	cfg, err := sdkconfig.Control(ctx, region)
	if err != nil {
		return Clients{}, fmt.Errorf("load aws credentials: %w", err)
	}
	return Clients{
		CFN:    cloudformation.NewFromConfig(cfg),
		Dynamo: dynamodb.NewFromConfig(cfg),
		KMS:    kms.NewFromConfig(cfg),
	}, nil
}

func Apply(ctx context.Context, clients Clients, req Request) (Response, error) {
	store, err := open(ctx, clients, req)
	if err != nil {
		return Response{}, err
	}
	records, err := req.records()
	if err != nil {
		return Response{}, err
	}
	result, err := store.PublishFor(ctx, req.Project, req.Publisher, req.Environment, records)
	if err != nil {
		return Response{}, err
	}
	return Response{Published: result.Published, Pruned: result.Pruned, Table: store.Table}, nil
}

func Destroy(ctx context.Context, clients Clients, req Request) (Response, error) {
	store, err := open(ctx, clients, req)
	if errors.Is(err, ErrNoSubstrate) {
		return Response{}, nil
	}
	if err != nil {
		return Response{}, err
	}
	result, err := store.PruneFor(ctx, req.Project, req.Publisher, req.Environment)
	if err != nil {
		return Response{}, err
	}
	return Response{Pruned: result.Pruned, Table: store.Table}, nil
}

func open(ctx context.Context, clients Clients, req Request) (*vars.Store, error) {
	if err := req.validate(); err != nil {
		return nil, err
	}
	deployed, err := Substrate(ctx, clients.CFN, req.Class)
	if err != nil {
		return nil, err
	}
	return &vars.Store{
		Dynamo: clients.Dynamo,
		KMS:    clients.KMS,
		Table:  deployed.VarsTable,
		KeyARN: deployed.VarsKeyARN,
		Class:  req.Class,
	}, nil
}
