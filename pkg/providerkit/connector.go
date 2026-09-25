package providerkit

import (
	"context"
	"slices"
	"strings"
)

const ConnectorConfigEnvVar = "OCEL_CONNECTOR_CONFIG_JSON"

type ConnectorTarget struct {
	Fingerprint string
	Hostname    string
	Arch        string
	Installed   *ConnectorRelease
}

type ConnectorRelease struct {
	Version   string
	PublicKey string
	Compute   Compute
}

type ConnectorInstall struct {
	Binary  []byte
	Version string
	Config  []byte
	Compute Compute
}

type ConnectorAddress struct {
	URL       string
	PublicKey string
	Compute   Compute
}

type Connector interface {
	Target(ctx context.Context) (ConnectorTarget, error)

	Install(ctx context.Context, install ConnectorInstall, progress Progress) (ConnectorAddress, error)

	Remove(ctx context.Context, progress Progress) error
}

func ConnectorCompute(requested Compute, supported ...Compute) (Compute, error) {
	if len(supported) == 0 {
		return "", Refuse(CodeInvalid, "this target stands no connector, so it hands out no compute to run one on")
	}
	if requested == "" {
		return supported[0], nil
	}
	if slices.Contains(supported, requested) {
		return requested, nil
	}
	return "", Refuse(CodeInvalid, "this target runs the connector on %s; %s is not a compute it hands out",
		strings.Join(ComputeNames(supported), " or "), requested)
}
