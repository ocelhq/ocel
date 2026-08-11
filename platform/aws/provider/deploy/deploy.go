package deploy

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"

	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
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
	Region      string
	BackendURL  string
	Passphrase  string
	ProjectName string
	StackName   string
	Pulumi      auto.PulumiCommand
	Secrets     SecretsReader

	Stacks StackIndex

	realized *realizedStacks

	StateTable     string
	StateTableARN  string
	VarsKeyARN     string
	VarsTable      string
	VarsTableARN   string
	VarsClass      string
	VarsReferenced map[vars.Coordinate]string
	Values         ValueStore

	ListenerCodePath string

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

	Slug               string
	StoreScriptName    string
	StoreEndpoint      string
	StoreBootstrapCred string

	ISRWriterEndpoint      string
	ISRWriterBootstrapCred string
	ISRWriterScriptName    string
	ISRWriterSeed          string

	Edge edge.Provider

	Class                  deploymentsv1.Environment_Class
	Lifecycle              deploymentsv1.Environment_Lifecycle
	Identity               string
	SharedClusterEndpoint  string
	SharedClusterSecretARN string

	ExpiresAt int64

	Tag string

	RootStackState edge.RootStackState
}

type Progress func(phase deploymentsv1.Phase, message string, current, total uint32)

func (p Progress) report(phase deploymentsv1.Phase, message string, current, total uint32) {
	if p != nil {
		p(phase, message, current, total)
	}
}

type Result struct {
	Outputs        []*deploymentsv1.ResourceOutput
	AppURLs        []string
	PromotionID    string
	RootStackState edge.RootStackState
}

func Run(ctx context.Context, cfg Config, manifest *deploymentsv1.Manifest, progress Progress, log func(string)) (Result, error) {
	cfg.realized = &realizedStacks{}
	return realize(ctx, cfg, manifest, progress, log)
}

func appURLs(manifest *deploymentsv1.Manifest, outputs []*deploymentsv1.ResourceOutput) []string {
	urlByLogical := make(map[string]string, len(outputs))
	for _, o := range outputs {
		if f := o.GetFunction(); f != nil && f.GetUrl() != "" {
			urlByLogical[o.GetLogicalName()] = f.GetUrl()
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

func collectPostgresOutput(ctx context.Context, secrets SecretsReader, name string, fields map[string]any) (*deploymentsv1.ResourceOutput, error) {
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

	return &deploymentsv1.ResourceOutput{
		LogicalName: name,
		Output: &deploymentsv1.ResourceOutput_Postgres{
			Postgres: &deploymentsv1.PostgresOutput{
				Host:     host,
				Port:     int32(port),
				Database: database,
				Username: username,
				Password: password,
			},
		},
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
