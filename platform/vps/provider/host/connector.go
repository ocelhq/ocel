package host

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/vps/provider/switchboard"
)

const (
	ConnectorBinary   = helperRoot + "/connector"
	connectorRoot     = classRoot + "/connector"
	ConnectorConfig   = connectorRoot + "/config.json"
	ConnectorKey      = connectorRoot + "/key"
	ConnectorUnit     = "ocel-connector.service"
	connectorUnitFile = "/etc/systemd/system/" + ConnectorUnit
	ConnectorRun      = "/run/ocel"
	ConnectorSocket   = ConnectorRun + "/connector.sock"
	connectorTmpfiles = "/etc/tmpfiles.d/ocel-connector.conf"
)

func connectorUnit() []byte {
	return []byte(strings.Join([]string{
		"[Unit]",
		"Description=the ocel connector this host answers the console over",
		"After=docker.service",
		"Wants=docker.service",
		"",
		"[Service]",
		"User=" + deployUser,
		"Group=" + deployUser,
		"ExecStart=" + ConnectorBinary + " --config " + ConnectorConfig + " --listen unix://" + ConnectorSocket,
		"Restart=on-failure",
		"RestartSec=5s",
		"",
		"[Install]",
		"WantedBy=multi-user.target",
		"",
	}, "\n"))
}

func connectorRuntimeConf() []byte {
	return []byte("d " + ConnectorRun + " 0755 " + deployUser + " " + deployUser + " -\n")
}

func ConnectorItems(binary, config []byte) []Item {
	unit := connectorUnit()
	return []Item{
		{Kind: KindFile, Name: ConnectorBinary, Mode: 0o755, Owner: rootOwner, Content: binary,
			Note: "console connector"},
		dir(classRoot, 0o755, rootOwner, "ocel's config root"),
		dir(connectorRoot, 0o700, stateOwner, ""),
		{Kind: KindFile, Name: ConnectorConfig, Mode: 0o600, Owner: stateOwner, Content: config,
			Note: "the console it trusts"},
		{Kind: KindFile, Name: connectorTmpfiles, Mode: 0o644, Owner: rootOwner, Content: connectorRuntimeConf()},
		dir(ConnectorRun, 0o755, stateOwner, ""),
		{Kind: KindFile, Name: connectorUnitFile, Mode: 0o644, Owner: rootOwner, Content: unit},
		{Kind: KindUnit, Name: ConnectorUnit, Owner: rootOwner, Content: unitWatchFacts(unit, binary, config),
			Watch: []string{connectorUnitFile, ConnectorBinary, ConnectorConfig},
			Slow:  true},
	}
}

type Connector struct{ host *Host }

func NewConnector(host *Host) *Connector { return &Connector{host: host} }

type ConnectorStanding struct {
	Installed bool
	Version   string
	PublicKey string
}

func (c *Connector) Describe(ctx context.Context) (ConnectorStanding, error) {
	rendered, err := c.host.run(ctx, "ask this host what connector it carries", connectorSurvey(), nil)
	if err != nil {
		return ConnectorStanding{}, err
	}
	return readConnectorStanding(rendered)
}

func connectorSurvey() string {
	printing := quoted(ConnectorBinary) + " --config " + quoted(ConnectorConfig) + " --print-public-key"
	return "if [ -x " + quoted(ConnectorBinary) + " ] && [ -f " + quoted(ConnectorConfig) + " ]; then\n" +
		"printf 'version=%s\\n' \"$(" + quoted(ConnectorBinary) + " --version 2>/dev/null || true)\"\n" +
		"printf 'key=%s\\n' \"$(su -s /bin/sh -c " + quoted(printing) + " " + quoted(deployUser) + " 2>/dev/null || true)\"\n" +
		"fi"
}

func readConnectorStanding(rendered string) (ConnectorStanding, error) {
	standing := ConnectorStanding{}
	for line := range strings.SplitSeq(strings.TrimSpace(rendered), "\n") {
		key, value, named := strings.Cut(strings.TrimSpace(line), "=")
		if !named {
			continue
		}
		switch key {
		case "version":
			standing.Installed, standing.Version = true, value
		case "key":
			standing.PublicKey = value
		}
	}
	if standing.Installed && standing.PublicKey == "" {
		return ConnectorStanding{}, providerkit.Refuse(providerkit.CodeNotReady,
			"%s answered no public key\nRemove the connector and add it again",
			ConnectorBinary)
	}
	return standing, nil
}

func keyPathed(config []byte) ([]byte, error) {
	var named map[string]any
	if err := json.Unmarshal(config, &named); err != nil {
		return nil, providerkit.Refuse(providerkit.CodeInvalid,
			"the connector config is not an object: %s", err)
	}
	named["keyPath"] = ConnectorKey
	written, err := json.Marshal(named)
	if err != nil {
		return nil, err
	}
	return append(written, '\n'), nil
}

func (c *Connector) Install(ctx context.Context, hostname string, binary, config []byte, report providerkit.Reporter) (ConnectorStanding, error) {
	written, err := keyPathed(config)
	if err != nil {
		return ConnectorStanding{}, err
	}
	items := ConnectorItems(binary, written)
	rendered, err := c.host.run(ctx, "survey the connector this host carries", survey(items), nil)
	if err != nil {
		return ConnectorStanding{}, err
	}
	observed, _, err := readSurvey(rendered)
	if err != nil {
		return ConnectorStanding{}, err
	}
	for _, item := range items {
		if observed[item.ID()] == item.Digest() {
			say(report, item.ID()+": "+reasonStanding)
			continue
		}
		if err := c.host.Install(ctx, item); err != nil {
			return ConnectorStanding{}, err
		}
		say(report, "wrote "+item.ID())
	}
	if err := c.Route(ctx, hostname); err != nil {
		return ConnectorStanding{}, err
	}
	say(report, "routed "+hostname+switchboard.ConnectorPath)
	return c.Describe(ctx)
}

func (c *Connector) Remove(ctx context.Context, report providerkit.Reporter) error {
	if err := c.Route(ctx, ""); err != nil {
		return err
	}
	say(report, "unrouted "+switchboard.ConnectorPath)
	if _, err := c.host.run(ctx, "take the connector off this host", connectorRemoval(), nil); err != nil {
		return err
	}
	say(report, "removed "+ConnectorUnit+", "+ConnectorBinary+" and "+connectorRoot)
	return nil
}

func connectorRemoval() string {
	unit := quoted(ConnectorUnit)
	return strings.Join([]string{
		"if systemctl cat " + unit + " >/dev/null 2>&1; then systemctl disable --now " + unit + " >/dev/null 2>&1 || true; fi",
		"rm -f " + quoted(connectorUnitFile),
		"systemctl daemon-reload >/dev/null 2>&1 || true",
		"rm -f " + quoted(ConnectorBinary),
		"rm -rf " + quoted(connectorRoot),
		"rm -f " + quoted(connectorTmpfiles),
		"rm -f " + quoted(ConnectorSocket),
	}, "\n")
}

func (c *Connector) Route(ctx context.Context, hostname string) error {
	return c.host.reshape(ctx, func(state RoutingTable) (RoutingTable, error) {
		state.Connector = hostname
		return state, nil
	})
}
