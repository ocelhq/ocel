package deploy

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"

	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	linksv1 "github.com/ocelhq/ocel/pkg/proto/common/links/v1"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/values"
	"github.com/ocelhq/ocel/platform/aws/provider/transform"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type SecretsReader interface {
	GetSecretValue(ctx context.Context, in *secretsmanager.GetSecretValueInput, optFns ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error)
}

type Config struct {
	Region        string
	BackendURL    string
	Passphrase    string
	PulumiProject string
	Secrets       SecretsReader

	Tags    TagSweeper
	Records providerkit.RecordStore

	RequiredFeatures []string

	StateTable     string
	StateTableARN  string
	VarsKeyARN     string
	AppBoundaryARN string
	Class          providerkit.Class
	VarsReferenced map[values.Coordinate]string

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

	Tier                   environmentv1.Tier
	Lifecycle              environmentv1.Lifecycle
	Identity               string
	SharedClusterEndpoint  string
	SharedClusterSecretARN string

	ExpiresAt int64

	Tag string

	StackState edge.StackState

	Transform transform.Evaluator
}

type RecordWaiter interface {
	Await(ctx context.Context, records []edge.Record, say func(string)) error
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
