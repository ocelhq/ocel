package providerkit

import "context"

type ConnectorTarget struct {
	Fingerprint string
	Hostname    string
	Arch        string
	Installed   *ConnectorRelease
}

type ConnectorRelease struct {
	Version   string
	PublicKey string
}

type ConnectorInstall struct {
	Binary  []byte
	Version string
	Config  []byte
}

type ConnectorAddress struct {
	URL       string
	PublicKey string
}

type ConnectorHost interface {
	DescribeConnectorTarget(ctx context.Context) (ConnectorTarget, error)

	InstallConnector(ctx context.Context, install ConnectorInstall, report Reporter) (ConnectorAddress, error)

	RemoveConnector(ctx context.Context, report Reporter) error
}
