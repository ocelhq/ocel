package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
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

	"github.com/ocelhq/ocel/pkg/endpoint"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/providerhost"
	"github.com/ocelhq/ocel/pkg/target"
	"github.com/ocelhq/ocel/pkg/variablestoreserver"
	"github.com/ocelhq/ocel/platform/aws/provider/account"
	awsconnector "github.com/ocelhq/ocel/platform/aws/provider/connector"
	"github.com/ocelhq/ocel/platform/aws/provider/control"
	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
	"github.com/ocelhq/ocel/platform/aws/provider/sdkconfig"
)

var version = "dev"

const runtimeAPIEnvVar = "AWS_LAMBDA_RUNTIME_API"

func main() {
	addr := flag.String("addr", "127.0.0.1:7777", "address to serve on")
	region := flag.String("region", os.Getenv(awsconnector.RegionEnvVar), "AWS region the target is bootstrapped in")
	config := flag.String("config", os.Getenv("OCEL_CONNECTOR_CONFIG"), "path to the endpoint config naming the console, this connector and the scopes it allows")
	keyParameter := flag.String("key-parameter", os.Getenv(awsconnector.KeyParameterEnvVar), "name of the SecureString parameter storing the key this connector signs heartbeats with")
	flag.Parse()

	if err := run(*addr, *region, *config, *keyParameter); err != nil {
		fmt.Fprintln(os.Stderr, "ocel connector:", err)
		os.Exit(1)
	}
}

func run(addr, region, config, keyParameter string) error {
	ctx := context.Background()

	trust, err := endpoint.ReadConfig(config)
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

	bootstraps := &bootstrapCache{
		namespace: account.Namespace(ns),
		stacks:    cloudformation.NewFromConfig(cfg),
		now:       time.Now,
		read:      map[environment.Tier]cachedBootstrap{},
	}

	host, err := providerhost.New(controlHost(cfg.Region))
	if err != nil {
		return err
	}
	defer host.Close()
	spec := endpoint.Spec{
		Config:     trust,
		Version:    version,
		Vendor:     "aws",
		Addr:       addr,
		ConfigPath: config,
		Mounts:     mounts(host, cfg, bootstraps),
	}
	if keyParameter != "" {
		if spec.Identity, err = readIdentity(ctx, ssm.NewFromConfig(cfg), keyParameter); err != nil {
			return err
		}
	}

	if os.Getenv(runtimeAPIEnvVar) == "" {
		return endpoint.Serve(spec)
	}

	handler, err := endpoint.New(spec)
	if err != nil {
		return err
	}
	lambda.StartWithOptions(newInvocationHandler(spec, httpadapter.NewV2(bufferingStreams(handler))), lambda.WithContext(ctx))
	return nil
}

func mounts(host *providerhost.Host, cfg aws.Config, bootstraps *bootstrapCache) []endpoint.Mount {
	return append(host.Mounts(), endpoint.VariablesMount(variableStore(cfg, bootstraps)))
}

func controlHost(region string) providerhost.Config {
	return providerhost.Config{
		Version: version,
		Base: func(context.Context, endpoint.Caller) (provider.Settings, error) {
			return provider.Settings{Options: provider.Options{"region": region}}, nil
		},
		Serves: control.ServedProcedures(),
		New: func(ctx context.Context, _ endpoint.Caller, _ string, settings provider.Settings) (provider.Provider, error) {
			p, err := control.New(ctx, settings)
			if err != nil {
				return nil, err
			}
			return p, nil
		},
	}
}

func variableStore(cfg aws.Config, bootstraps *bootstrapCache) variablestoreserver.FixedBackend {
	return variablestoreserver.FixedBackend{
		KeyValues:     awsports.KeyValues{Dynamo: dynamodb.NewFromConfig(cfg), Tables: bootstraps},
		Cipher:        awsports.Cipher{KMS: kms.NewFromConfig(cfg), Keys: bootstraps},
		ProveIdentity: awsports.CallerIdentity{Config: cfg}.Prove,
	}
}

type keyReader interface {
	GetParameter(ctx context.Context, in *ssm.GetParameterInput, optFns ...func(*ssm.Options)) (*ssm.GetParameterOutput, error)
}

func readIdentity(ctx context.Context, store keyReader, name string) (endpoint.Identity, error) {
	stored, err := store.GetParameter(ctx, &ssm.GetParameterInput{Name: aws.String(name), WithDecryption: aws.Bool(true)})
	if err != nil {
		return endpoint.Identity{}, fmt.Errorf("read the connector's key from %s: %w", name, err)
	}
	return decodeIdentity(aws.ToString(stored.Parameter.Value))
}

func decodeIdentity(value string) (endpoint.Identity, error) {
	seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(value))
	if err != nil {
		return endpoint.Identity{}, fmt.Errorf("the connector's key is not base64: %w", err)
	}
	return endpoint.IdentityFromSeed(seed)
}

type proxy interface {
	ProxyWithContext(ctx context.Context, req events.APIGatewayV2HTTPRequest) (events.APIGatewayV2HTTPResponse, error)
}

func newInvocationHandler(spec endpoint.Spec, serve proxy) func(context.Context, json.RawMessage) (any, error) {
	return func(ctx context.Context, raw json.RawMessage) (any, error) {
		if isHeartbeatWake(raw) {
			err := endpoint.Heartbeat(ctx, spec)
			switch {
			case endpoint.IsRefusal(err):
				fmt.Printf("connector %s: heartbeat was refused: %v\n", spec.EndpointID, err)
			case err != nil:
				return nil, err
			default:
				fmt.Printf("connector %s: heartbeat answered\n", spec.EndpointID)
			}
			return nil, nil
		}
		var req events.APIGatewayV2HTTPRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, fmt.Errorf("read the invocation: %w", err)
		}
		return serve.ProxyWithContext(ctx, req)
	}
}

func isHeartbeatWake(raw []byte) bool {
	var wake awsconnector.Wake
	return json.Unmarshal(raw, &wake) == nil && wake.Ocel == awsconnector.WakeHeartbeat
}
