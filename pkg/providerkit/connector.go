package providerkit

import "context"

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

type ConnectorHost interface {
	DescribeConnectorTarget(ctx context.Context) (ConnectorTarget, error)

	InstallConnector(ctx context.Context, install ConnectorInstall, report Reporter) (ConnectorAddress, error)

	RemoveConnector(ctx context.Context, report Reporter) error
}
