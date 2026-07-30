package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/kms"

	"github.com/ocelhq/ocel/cloud/aws/vars/baked"
)

// keyUnwrapper is the one KMS call the runtime makes: unwrapping the data key
// this function's bundle was sealed under. Only the key travels; the values
// themselves are decrypted here, so their plaintext never leaves the sandbox.
type keyUnwrapper interface {
	Decrypt(ctx context.Context, in *kms.DecryptInput, optFns ...func(*kms.Options)) (*kms.DecryptOutput, error)
}

// bakedVarsEnv opens this function's encrypted-baked values and renders them
// as environment entries for the application process, under the namespaced
// name the SDK reads them back from — never under the key the user chose,
// which is what keeps them out of anything that dumps the environment expecting
// plaintext.
//
// Every failure is returned rather than absorbed. A variable that is silently
// unset is read at the point of use as one that was never required, so an app
// that cannot open its bundle must not come up at all.
func bakedVarsEnv(ctx context.Context, unwrap keyUnwrapper, envelope, taskRoot string) ([]string, error) {
	if envelope == "" {
		return nil, nil
	}
	wrapped, err := base64.StdEncoding.DecodeString(envelope)
	if err != nil {
		return nil, fmt.Errorf("%s is not a wrapped data key: %w", baked.EnvelopeVar, err)
	}
	path := filepath.Join(taskRoot, baked.FilePath)
	sealed, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", baked.FilePath, err)
	}
	out, err := unwrap.Decrypt(ctx, &kms.DecryptInput{CiphertextBlob: wrapped})
	if err != nil {
		return nil, fmt.Errorf("unwrap the data key for %s: %w", baked.FilePath, err)
	}
	values, err := baked.Open(out.Plaintext, sealed)
	if err != nil {
		return nil, err
	}

	env := make([]string, 0, len(values))
	for key, value := range values {
		env = append(env, baked.Prefix+key+"="+value)
	}
	sort.Strings(env)
	return env, nil
}

// resolveBakedVarsEnv is the wiring bakedVarsEnv is tested without: it builds
// the KMS client only when this function actually carries a sealed bundle, so
// an app without one pays neither the credential chain nor the call.
func resolveBakedVarsEnv(ctx context.Context) ([]string, error) {
	envelope := os.Getenv(baked.EnvelopeVar)
	if envelope == "" {
		return nil, nil
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}
	return bakedVarsEnv(ctx, kms.NewFromConfig(cfg), envelope, taskRoot())
}

// taskRoot is where the platform unpacked the deployment package.
func taskRoot() string {
	if root := os.Getenv("LAMBDA_TASK_ROOT"); root != "" {
		return root
	}
	return "/var/task"
}
