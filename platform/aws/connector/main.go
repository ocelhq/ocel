package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/awslabs/aws-lambda-go-api-proxy/httpadapter"

	"github.com/ocelhq/ocel/pkg/connectorkit"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/target"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	"github.com/ocelhq/ocel/platform/aws/provider/cfn"
	awsconnector "github.com/ocelhq/ocel/platform/aws/provider/connector"
	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
	"github.com/ocelhq/ocel/platform/aws/provider/sdkconfig"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

var version = "dev"

const runtimeAPIEnvVar = "AWS_LAMBDA_RUNTIME_API"

func main() {
	addr := flag.String("addr", "127.0.0.1:7777", "address to serve on")
	region := flag.String("region", os.Getenv(edge.AWSRegionVar), "AWS region the target is bootstrapped in")
	config := flag.String("config", os.Getenv("OCEL_CONNECTOR_CONFIG"), "path to the connector config naming the console, this connector and its grants")
	keyParameter := flag.String("key-parameter", os.Getenv(awsconnector.KeyParameterEnvVar), "name of the SecureString parameter holding the key this connector signs heartbeats with")
	flag.Parse()

	if err := run(*addr, *region, *config, *keyParameter); err != nil {
		fmt.Fprintln(os.Stderr, "ocel connector:", err)
		os.Exit(1)
	}
}

func run(addr, region, config, keyParameter string) error {
	ctx := context.Background()

	trust, err := connectorkit.ReadConfig(config)
	if err != nil {
		return err
	}

	ns, err := provider.NamespaceFromEnv()
	if err != nil {
		return err
	}
	cfg, err := sdkconfig.Control(ctx, region)
	if err != nil {
		return err
	}

	who, err := sts.NewFromConfig(cfg).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return fmt.Errorf("resolve AWS account id: %w", err)
	}
	trust.Target, err = target.Fingerprint("aws", aws.ToString(who.Account), cfg.Region, string(ns))
	if err != nil {
		return err
	}

	held := &deployments{
		namespace: bootstrap.Namespace(ns),
		stacks:    cloudformation.NewFromConfig(cfg),
		now:       time.Now,
		read:      map[edge.Class]readDeployment{},
	}

	spec := connectorkit.Spec{
		Config:     trust,
		Version:    version,
		Vendor:     "aws",
		Addr:       addr,
		ConfigPath: config,
		Vars: providerkit.Vars{
			Records: awsports.Records{Dynamo: dynamodb.NewFromConfig(cfg), Tables: held},
			Cipher:  awsports.Cipher{KMS: kms.NewFromConfig(cfg), Keys: held},
		},
	}
	if keyParameter != "" {
		if spec.Identity, err = keyed(ctx, ssm.NewFromConfig(cfg), keyParameter); err != nil {
			return err
		}
	}

	if os.Getenv(runtimeAPIEnvVar) == "" {
		return connectorkit.Serve(spec)
	}

	mux, err := connectorkit.Mux(spec)
	if err != nil {
		return err
	}
	lambda.StartWithOptions(invoked(spec, httpadapter.NewV2(mux)), lambda.WithContext(ctx))
	return nil
}

const deploymentsTTL = 5 * time.Minute

type keyReader interface {
	GetParameter(ctx context.Context, in *ssm.GetParameterInput, optFns ...func(*ssm.Options)) (*ssm.GetParameterOutput, error)
}

func keyed(ctx context.Context, store keyReader, name string) (connectorkit.Identity, error) {
	held, err := store.GetParameter(ctx, &ssm.GetParameterInput{Name: aws.String(name), WithDecryption: aws.Bool(true)})
	if err != nil {
		return connectorkit.Identity{}, fmt.Errorf("read the connector's key from %s: %w", name, err)
	}
	return identityOf(aws.ToString(held.Parameter.Value))
}

func identityOf(value string) (connectorkit.Identity, error) {
	seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(value))
	if err != nil {
		return connectorkit.Identity{}, fmt.Errorf("the connector's key is not base64: %w", err)
	}
	return connectorkit.IdentityFromSeed(seed)
}

type proxy interface {
	ProxyWithContext(ctx context.Context, req events.APIGatewayV2HTTPRequest) (events.APIGatewayV2HTTPResponse, error)
}

func invoked(spec connectorkit.Spec, serve proxy) func(context.Context, json.RawMessage) (any, error) {
	return func(ctx context.Context, raw json.RawMessage) (any, error) {
		if woken(raw) {
			status, err := connectorkit.Heartbeat(ctx, spec)
			if err != nil {
				return nil, err
			}
			fmt.Printf("connector %s: heartbeat answered %d\n", spec.ConnectorID, status)
			return nil, nil
		}
		var req events.APIGatewayV2HTTPRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, fmt.Errorf("read the invocation: %w", err)
		}
		return serve.ProxyWithContext(ctx, req)
	}
}

func woken(raw []byte) bool {
	var wake awsconnector.Wake
	return json.Unmarshal(raw, &wake) == nil && wake.Ocel == awsconnector.WakeHeartbeat
}

type deployments struct {
	namespace bootstrap.Namespace
	stacks    cfn.StacksAPI
	now       func() time.Time

	mu   sync.Mutex
	read map[edge.Class]readDeployment
}

type readDeployment struct {
	held bootstrap.Deployed
	at   time.Time
}

func (d *deployments) resolve(ctx context.Context, class edge.Class) (bootstrap.Deployed, error) {
	d.mu.Lock()
	memo, known := d.read[class]
	d.mu.Unlock()
	if known && d.now().Sub(memo.at) < deploymentsTTL {
		return memo.held, nil
	}
	held, err := bootstrap.CheckDeployedFor(ctx, d.stacks, d.namespace, string(class))
	if err != nil {
		return bootstrap.Deployed{}, err
	}
	d.mu.Lock()
	d.read[class] = readDeployment{held: held, at: d.now()}
	d.mu.Unlock()
	return held, nil
}

func (d *deployments) Table(ctx context.Context, class edge.Class) (string, error) {
	held, err := d.resolve(ctx, class)
	return held.StateTable, err
}

func (d *deployments) ValuesTable(ctx context.Context, class edge.Class) (string, error) {
	held, err := d.resolve(ctx, class)
	return held.VarsTable, err
}

func (d *deployments) Key(ctx context.Context, class edge.Class) (string, error) {
	held, err := d.resolve(ctx, class)
	return held.VarsKeyARN, err
}
