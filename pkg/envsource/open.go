package envsource

import (
	"fmt"
	"os/exec"
	"strings"
)

const infisicalTokenEnvVar = "INFISICAL_TOKEN"

func Open(descriptor Descriptor, dir string, lookupEnv func(string) (string, bool)) (Source, error) {
	switch {
	case descriptor.Kind == Exec && descriptor.Exec != nil:
		return execSource{options: *descriptor.Exec, dir: dir}, nil
	case descriptor.Kind == Infisical && descriptor.Infisical != nil:
		return openInfisicalAsDeveloper(*descriptor.Infisical, dir, lookupEnv)
	}
	return nil, fmt.Errorf("a %s env source is ocel's own to read, never a command or service to open", descriptor.Kind)
}

func openInfisicalAsDeveloper(options InfisicalOptions, dir string, lookupEnv func(string) (string, bool)) (Source, error) {
	options = options.Normalize()
	options.Write = WriteNever
	if token, set := lookupEnv(infisicalTokenEnvVar); set && strings.TrimSpace(token) != "" {
		return NewInfisical(options, AccessToken(strings.TrimSpace(token)), nil), nil
	}
	cli, err := exec.LookPath(infisicalCLIName)
	if err != nil {
		return nil, fmt.Errorf("%s is read as you, and this shell offers no way in: export %s with an access token, or install the infisical CLI and run `infisical login`", options.ID(), infisicalTokenEnvVar)
	}
	return infisicalExport{options: options, cli: cli, dir: dir}, nil
}
