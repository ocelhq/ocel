package deploy

import (
	"context"
	"fmt"

	awsec2 "github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/pulumi/pulumi/sdk/v3/go/auto"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/resources"
	"github.com/ocelhq/ocel/pkg/provider/transform"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/variablestore"
	"github.com/ocelhq/ocel/platform/aws/provider/payloads"
)

type Config struct {
	Region        string
	BackendURL    string
	Passphrase    string
	PulumiProject string
	Parameters    ParametersAPI
	KVTokenRoot   string
	Clusters      ClustersAPI

	SigningKeys    SigningKeysAPI
	SigningKeyRoot string

	Tags      TagClock
	KeyValues records.Store
	WrittenBy provider.WrittenBy
	Rules     RulesAPI
	Subnets   awsec2.DescribeSubnetsAPIClient

	RequiredParts []string

	StateTable          string
	StateTableARN       string
	VariablesTable      string
	VariablesTableARN   string
	VariablesKeyARN     string
	AppBoundaryARN      string
	AppRolePath         string
	Tier                environment.Tier
	VariablesReferenced map[variablestore.Coordinate]string

	RuntimeLayers map[string]string

	BytecodeEmbed bool

	ArtifactRoot   string
	ArtifactBucket string
	Objects        payloads.ObjectStore

	Invoker InvokeAPI

	Getter      ObjectsAPI
	CodeUpdater FunctionCodeAPI

	AssetBucket string

	ImageOptimizerURL string

	Env string

	EdgeAccessKeyID    string
	EdgeSecretKey      string
	EdgeCredentialsErr error

	GlobalPreviewDomain string

	Slug                     string
	StoreScriptName          string
	StoreEndpoint            string
	StoreBootstrapCredential string

	PurgeSeed string

	OriginSecret         string
	PreviousOriginSecret string

	Edge edge.Edge

	DNS      edge.DNSRecords
	DNSAwait Propagation

	ExpiresAt int64

	Tag string

	StackState edge.StackState

	Transform transform.Pass

	Retention *resources.ImageRetentionHooks
}

type Propagation interface {
	Await(ctx context.Context, dnsRecords []edge.Record, say func(string)) error
}

func requireOutputFields(outputs auto.OutputMap, name string) (map[string]any, error) {
	raw, produced := outputs[name]
	if !produced {
		return nil, fmt.Errorf("stack produced no output for %s", name)
	}
	fields, mapped := raw.Value.(map[string]any)
	if !mapped {
		return nil, fmt.Errorf("output for %s is not a map", name)
	}
	return fields, nil
}

func requireStringField(fields map[string]any, name, key string) (string, error) {
	v, ok := fields[key].(string)
	if !ok || v == "" {
		return "", fmt.Errorf("output %q for %s is missing or not a non-empty string", key, name)
	}
	return v, nil
}
