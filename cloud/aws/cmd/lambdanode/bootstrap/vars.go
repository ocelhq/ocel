package main

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/ocelhq/ocel/cloud/aws/vars/baked"
)

// bakedVarsEnv opens this function's encrypted-baked values and renders them
// as environment entries for the application process, under the namespaced
// name the SDK reads them back from — never under the key the user chose,
// which is what keeps them out of anything that dumps the environment expecting
// plaintext.
//
// The data key arrives in the function's own configuration, so opening the
// bundle is pure local work: no client, no credentials, no call on the init
// path an application's cold start is already paying for.
//
// Every failure is returned rather than absorbed. A variable that is silently
// unset is read at the point of use as one that was never required, so an app
// that cannot open its bundle must not come up at all.
func bakedVarsEnv(envelope, taskRoot string) ([]string, error) {
	if envelope == "" {
		return nil, nil
	}
	key, err := base64.StdEncoding.DecodeString(envelope)
	if err != nil {
		return nil, fmt.Errorf("%s is not a data key: %w", baked.EnvelopeVar, err)
	}
	sealed, err := os.ReadFile(filepath.Join(taskRoot, baked.FilePath))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", baked.FilePath, err)
	}
	values, err := baked.Open(key, sealed)
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

// resolveBakedVarsEnv is the wiring bakedVarsEnv is tested without: where the
// envelope and the package are found in a live sandbox.
func resolveBakedVarsEnv() ([]string, error) {
	return bakedVarsEnv(os.Getenv(baked.EnvelopeVar), taskRoot())
}

// taskRoot is where the platform unpacked the deployment package.
func taskRoot() string {
	if root := os.Getenv("LAMBDA_TASK_ROOT"); root != "" {
		return root
	}
	return "/var/task"
}
