package deploy

import (
	"context"

	"github.com/ocelhq/ocel/platform/aws/provider/resources/variables"

	awsec2 "github.com/aws/aws-sdk-go-v2/service/ec2"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/resources"
	"github.com/ocelhq/ocel/pkg/provider/transform"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/variablestore"
	"github.com/ocelhq/ocel/platform/aws/provider/payloads"
	"github.com/ocelhq/ocel/platform/aws/provider/resources/kv"
	"github.com/ocelhq/ocel/platform/aws/provider/resources/postgres"
	"github.com/ocelhq/ocel/platform/aws/provider/resources/realtime"
	"github.com/ocelhq/ocel/platform/aws/provider/resources/topic"
)

type Config struct {
	Region        string
	BackendURL    string
	Passphrase    string
	PulumiProject string
	Parameters    kv.ParametersAPI
	KVTokenRoot   string
	Clusters      postgres.ClustersAPI

	SigningKeys    realtime.SigningKeysAPI
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

func (c Config) variableStore() variables.Store {
	return variables.Store{Table: c.VariablesTable, KeyARN: c.VariablesKeyARN, Tier: c.Tier, Env: c.Env, Referenced: c.VariablesReferenced}
}

func (c Config) stateTable() topic.StateTable {
	return topic.StateTable{Name: c.StateTable, ARN: c.StateTableARN, Region: c.Region}
}

type Propagation interface {
	Await(ctx context.Context, dnsRecords []edge.Record, say func(string)) error
}
