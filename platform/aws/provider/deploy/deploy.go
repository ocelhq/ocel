package deploy

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"

	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	linksv1 "github.com/ocelhq/ocel/pkg/proto/links/v1"
	"github.com/ocelhq/ocel/platform/aws/provider/payloads"
	"github.com/ocelhq/ocel/platform/aws/provider/transform"
	"github.com/ocelhq/ocel/platform/aws/provider/vars"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func pulumiEnv(region, backendURL, passphrase string) map[string]string {
	return map[string]string{
		"PULUMI_BACKEND_URL":       backendURL,
		"PULUMI_CONFIG_PASSPHRASE": passphrase,
		"AWS_REGION":               region,
		"PULUMI_SKIP_CHECKPOINTS":  "true",
		"PULUMI_SKIP_UPDATE_CHECK": "true",
	}
}

type SecretsReader interface {
	GetSecretValue(ctx context.Context, in *secretsmanager.GetSecretValueInput, optFns ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error)
}

type Config struct {
	Region        string
	BackendURL    string
	Passphrase    string
	PulumiProject string
	Pulumi        auto.PulumiCommand
	Secrets       SecretsReader

	Stacks StackIndex

	realized *realizedStacks

	StateTable         string
	StateTableARN      string
	VarsKeyARN         string
	VarsTable          string
	VarsTableARN       string
	VarsClass          string
	VarsSiblingClasses []string
	VarsReferenced     map[vars.Coordinate]string
	Values             ValueStore
	Links              LinkStore

	ArtifactRoot   string
	ArtifactBucket string
	Uploader       ArtifactUploader

	Invoker FunctionInvoker

	Getter      ObjectGetter
	CodeUpdater FunctionCodeUpdater

	AssetBucket string

	ImageOptimizerURL string

	RevalidateQueueURL string

	CacheStoreBucket   string
	CacheStoreUploader ArtifactUploader
	Env                string

	EdgeAccessKeyID string
	EdgeSecretKey   string

	EdgeValues map[string]string

	GlobalPreviewDomain string

	Slug               string
	StoreScriptName    string
	StoreEndpoint      string
	StoreBootstrapCred string

	ISRWriterEndpoint      string
	ISRWriterBootstrapCred string
	ISRWriterScriptName    string
	ISRWriterSeed          string

	OriginSecret string

	Edge edge.Edge

	DNS      edge.DNSWriter
	DNSAwait RecordWaiter

	AllowDegraded []string
	Degraded      func(need edge.Need, detail string)

	Class                  deploymentsv1.Environment_Class
	Lifecycle              deploymentsv1.Environment_Lifecycle
	Identity               string
	SharedClusterEndpoint  string
	SharedClusterSecretARN string

	ExpiresAt int64

	Tag string

	StackState edge.StackState

	Stages      Stages
	AppStages   map[string]Stage
	Tracer      Tracer
	StageReport func(StageID) func(string)

	Transform transform.Evaluator

	transformed *transformedArgs
	needs       needRecords
	sessions    sessionScope
	layer       payloads.Placement
	completer   payloads.Placement
}

type RecordWaiter interface {
	Await(ctx context.Context, records []edge.Record, say func(string)) error
}

type Stages struct {
	Uploading    Stage
	Provisioning Stage
	Finalizing   Stage
}

func AppStages(provisioning Stage, manifest *deploymentsv1.Manifest) (map[string]Stage, []Stage) {
	apps := manifestApps(manifest)
	byApp := make(map[string]Stage, len(apps))
	declared := make([]Stage, 0, len(apps))
	for _, app := range apps {
		s := NewStage(provisioning, app.GetName())
		byApp[app.GetName()] = s
		declared = append(declared, s)
	}
	return byApp, declared
}

type Progress func(phase deploymentsv1.Phase, message string, current, total uint32)

func (p Progress) report(phase deploymentsv1.Phase, message string, current, total uint32) {
	if p != nil {
		p(phase, message, current, total)
	}
}

type Result struct {
	Links       []*linksv1.Link
	Functions   []*deploymentsv1.FunctionOutput
	AppURLs     []string
	URLNote     string
	PromotionID string
	Flip        *edge.FlipBound
	StackState  edge.StackState
}

func Run(ctx context.Context, cfg Config, manifest *deploymentsv1.Manifest, progress Progress, log func(string)) (Result, error) {
	cfg.realized = &realizedStacks{}
	return realize(ctx, cfg, manifest, progress, log)
}

func appURLs(manifest *deploymentsv1.Manifest, functions []*deploymentsv1.FunctionOutput) []string {
	urlByLogical := make(map[string]string, len(functions))
	for _, f := range functions {
		if f.GetUrl() != "" {
			urlByLogical[f.GetLogicalName()] = f.GetUrl()
		}
	}

	var urls []string
	for _, app := range manifestApps(manifest) {
		if worker := urlByLogical[workerOutputName(app.GetName())]; worker != "" {
			urls = append(urls, worker)
			continue
		}
		for _, fn := range manifest.GetFunctions() {
			if fn.GetApp() != app.GetName() {
				continue
			}
			if url := urlByLogical[fn.GetLogicalName()]; url != "" {
				urls = append(urls, url)
			}
		}
	}
	return urls
}

func stampExpiry(ctx context.Context, stack auto.Stack, expiresAt int64) error {
	if expiresAt == 0 {
		return nil
	}
	_ = stack
	return nil
}

func collectPostgresLink(ctx context.Context, secrets SecretsReader, name string, fields map[string]any) (*linksv1.Link, error) {
	host, err := requireStringField(fields, name, outputKeyHost)
	if err != nil {
		return nil, err
	}
	database, err := requireStringField(fields, name, outputKeyDatabase)
	if err != nil {
		return nil, err
	}
	username, err := requireStringField(fields, name, outputKeyUsername)
	if err != nil {
		return nil, err
	}
	secretARN, err := requireStringField(fields, name, outputKeySecretARN)
	if err != nil {
		return nil, err
	}

	port := postgresPort
	if p, ok := fields[outputKeyPort].(float64); ok {
		port = int(p)
	}

	password, err := resolveManagedPassword(ctx, secrets, secretARN)
	if err != nil {
		return nil, fmt.Errorf("resolve master password for %s: %w", name, err)
	}

	return &linksv1.Link{
		Name: name,
		Properties: &linksv1.Link_Postgres{Postgres: &linksv1.PostgresProperties{
			Host:     host,
			Port:     int32(port),
			Database: database,
			Username: username,
			Password: password,
		}},
	}, nil
}

func requireStringField(fields map[string]any, name, key string) (string, error) {
	v, ok := fields[key].(string)
	if !ok || v == "" {
		return "", fmt.Errorf("output %q for %s is missing or not a non-empty string", key, name)
	}
	return v, nil
}

func resolveManagedPassword(ctx context.Context, secrets SecretsReader, secretARN string) (string, error) {
	if secretARN == "" {
		return "", fmt.Errorf("empty master-user secret ARN")
	}
	if secrets == nil {
		return "", fmt.Errorf("no Secrets Manager client configured")
	}
	out, err := secrets.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{SecretId: &secretARN})
	if err != nil {
		return "", err
	}
	if out.SecretString == nil {
		return "", fmt.Errorf("secret %s has no string value", secretARN)
	}
	var parsed struct {
		Password string `json:"password"`
	}
	if err := json.Unmarshal([]byte(*out.SecretString), &parsed); err != nil {
		return "", fmt.Errorf("parse managed secret: %w", err)
	}
	return parsed.Password, nil
}
