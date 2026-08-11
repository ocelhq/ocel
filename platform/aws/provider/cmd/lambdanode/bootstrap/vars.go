package main

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/ocelhq/ocel/platform/aws/provider/vars/baked"
)

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
	slices.Sort(env)
	return env, nil
}

func resolveBakedVarsEnv() ([]string, error) {
	return bakedVarsEnv(os.Getenv(baked.EnvelopeVar), taskRoot())
}

func taskRoot() string {
	if root := os.Getenv("LAMBDA_TASK_ROOT"); root != "" {
		return root
	}
	return "/var/task"
}
