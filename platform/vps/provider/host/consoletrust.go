package host

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/machine"
	"github.com/ocelhq/ocel/platform/vps/provider/shell"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

const (
	SSHDDropIn         = "/etc/ssh/sshd_config.d/ocel.conf"
	authorityComment   = "ocel-user-ca"
	trustAbsent        = "absent"
	connectorInstalled = "installed"
)

func sshdDropIn() []byte {
	return []byte(strings.Join([]string{
		"Match User " + deployUser,
		"\tTrustedUserCAKeys " + boxapi.AuthorityPublicKey,
		"\tAuthorizedPrincipalsFile " + boxapi.AuthorizedPrincipals,
		"Match all",
		"",
	}, "\n"))
}

func authorityConfig() []byte {
	written, err := json.Marshal(boxapi.AuthorityConfig{Principal: deployUser})
	if err != nil {
		panic(err)
	}
	return append(written, '\n')
}

func consoleTrustContent(trust provider.WebIdentityTrust) []byte {
	written, err := json.Marshal(boxapi.ConsoleTrust{
		Issuer:         trust.Issuer,
		Audience:       trust.Audience,
		OrganizationID: trust.OrganizationID,
		TargetID:       trust.TargetID,
	})
	if err != nil {
		panic(err)
	}
	return append(written, '\n')
}

const sudoersSign = machine.SudoersRoot + "/ocel-ssh-sign"

func signSudoers() []byte {
	return []byte(deployUser + " ALL=(root) NOPASSWD: " + LiveBinary + " " + boxapi.SignCommand + "\n")
}

func authorityKey() machine.Item {
	return machine.Item{Kind: machine.KindAuthorityKey, Name: boxapi.AuthorityKey, Mode: 0o600, Owner: machine.RootOwner,
		Note: "signs the ssh certificates the console's credentials are exchanged for", Script: mintAuthorityKey, Survey: authoritySurvey}
}

func mintAuthorityKey(i machine.Item) string {
	key := shell.Quote(i.Name)
	public := shell.Quote(boxapi.AuthorityPublicKey)
	return strings.Join([]string{
		"set -e",
		"if [ ! -f " + key + " ]; then rm -f " + public + "; ssh-keygen -q -t ed25519 -N '' -C " + authorityComment + " -f " + key + "; fi",
		"chown root:root " + key,
		"chmod 0600 " + key,
		"ssh-keygen -y -f " + key + " | install -m 0644 -o root -g root /dev/stdin " + public,
	}, "\n")
}

func authoritySurvey(i machine.Item) string {
	name := shell.Quote(i.Name)
	return "if [ -f " + name + " ]; then " +
		machine.Reports(shell.Quote(i.Kind), name, `"$(stat -c %a `+name+`)"`, `"$(stat -c %U `+name+`)"`, `''`) + "; fi"
}

func AuthorityItems() []machine.Item {
	return []machine.Item{
		machine.Dir(boxapi.AuthorityDir, 0o755, machine.RootOwner, ""),
		authorityKey(),
		{Kind: machine.KindFile, Name: boxapi.AuthorityConfigPath, Mode: 0o644, Owner: machine.RootOwner, Content: authorityConfig(),
			Note: "the login its certificates name"},
		{Kind: machine.KindFile, Name: boxapi.AuthorizedPrincipals, Mode: 0o644, Owner: machine.RootOwner, Content: []byte(deployUser + "\n"),
			Note: "the only login sshd lets its certificates in as"},
		{Kind: machine.KindFile, Name: sudoersSign, Mode: 0o440, Owner: machine.RootOwner, Content: signSudoers(),
			Note: "the connector signs certificates through ocel-live, never reading the key"},
		{Kind: machine.KindFile, Name: SSHDDropIn, Mode: 0o644, Owner: machine.RootOwner, Content: sshdDropIn(),
			Note: "sshd trusts the box's certificates for " + deployUser + " alone"},
	}
}

func ConsoleTrustItems(tier environment.Tier, trust provider.WebIdentityTrust) []machine.Item {
	return append(AuthorityItems(), machine.Item{
		Kind: machine.KindFile, Name: boxapi.ConsoleTrustPath(tier), Mode: 0o644, Owner: machine.RootOwner, Content: consoleTrustContent(trust),
		Note: "the console whose credentials the box's connector exchanges for certificates",
	})
}

type ConsoleTrust struct{ host *Host }

func (h *Host) ConsoleTrust() *ConsoleTrust { return &ConsoleTrust{host: h} }

type TrustReading struct {
	Trust    *provider.WebIdentityTrust
	Sibling  *provider.WebIdentityTrust
	Observed map[string]string

	ConnectorOrganization string
}

func (r TrustReading) IsCurrent(items []machine.Item) bool {
	for _, item := range items {
		if r.Observed[item.ID()] != item.Digest() {
			return false
		}
	}
	return len(items) > 0
}

func (r TrustReading) ListChanges(items []machine.Item) []provider.Change {
	changes := make([]provider.Change, 0, len(items))
	for _, item := range items {
		change := provider.Change{Kind: item.Kind, Name: item.Name, Action: provider.ActionCreate, Reason: item.Note}
		digest, seen := r.Observed[item.ID()]
		switch {
		case seen && digest == item.Digest():
			change.Action, change.Reason = provider.ActionKeep, machine.ReasonCurrent
		case seen:
			change.Action = provider.ActionUpdate
		}
		changes = append(changes, change)
	}
	return changes
}

func (c *ConsoleTrust) Read(ctx context.Context, tier environment.Tier) (TrustReading, error) {
	surveying := ConsoleTrustItems(tier, provider.WebIdentityTrust{})
	rendered, err := c.host.Run(ctx, ssh.RootWhenAble, "survey the console trust "+string(tier)+" has", machine.SurveyScript(surveying), nil)
	if err != nil {
		return TrustReading{}, err
	}
	observed, _, err := machine.ReadSurvey(rendered)
	if err != nil {
		return TrustReading{}, err
	}
	if c.host.Elevation(ctx) != nil {
		if err := c.addRecordedDigests(ctx, tier, observed); err != nil {
			return TrustReading{}, err
		}
	}
	reading := TrustReading{Observed: observed}
	if reading.Trust, err = c.readTrust(ctx, tier); err != nil {
		return TrustReading{}, err
	}
	if reading.Sibling, err = c.readTrust(ctx, tier.Sibling()); err != nil {
		return TrustReading{}, err
	}
	if reading.ConnectorOrganization, err = c.readConnectorOrganization(ctx); err != nil {
		return TrustReading{}, err
	}
	return reading, nil
}

func (c *ConsoleTrust) readTrust(ctx context.Context, tier environment.Tier) (*provider.WebIdentityTrust, error) {
	said, err := c.readFile(ctx, "read the console "+string(tier)+" trusts", boxapi.ConsoleTrustPath(tier))
	if err != nil || said == "" {
		return nil, err
	}
	var recorded boxapi.ConsoleTrust
	if err := json.Unmarshal([]byte(said), &recorded); err != nil {
		return nil, refusal.Refuse(refusal.CodeInvalid,
			"%s is not the console trust ocel writes: %s\nRun `%s` again", boxapi.ConsoleTrustPath(tier), err, provider.BootstrapCommand(tier))
	}
	return &provider.WebIdentityTrust{
		Issuer:         recorded.Issuer,
		Audience:       recorded.Audience,
		OrganizationID: recorded.OrganizationID,
		TargetID:       recorded.TargetID,
	}, nil
}

func consoleTrustDigests(tier environment.Tier, trust provider.WebIdentityTrust) machine.Item {
	written, err := json.Marshal(machine.DigestsOf(ConsoleTrustItems(tier, trust)))
	if err != nil {
		panic(err)
	}
	return machine.Item{Kind: machine.KindFile, Name: boxapi.ConsoleTrustDigestsPath(tier), Mode: 0o644, Owner: machine.RootOwner, Content: append(written, '\n'),
		Note: "what the trust part wrote, for a login that cannot read it all"}
}

func (c *ConsoleTrust) addRecordedDigests(ctx context.Context, tier environment.Tier, observed map[string]string) error {
	said, err := c.readFile(ctx, "read what the "+string(tier)+" console trust recorded", boxapi.ConsoleTrustDigestsPath(tier))
	if err != nil || said == "" {
		return err
	}
	var recorded map[string]string
	if err := json.Unmarshal([]byte(said), &recorded); err != nil {
		return nil
	}
	for id, digest := range recorded {
		if _, seen := observed[id]; !seen {
			observed[id] = digest
		}
	}
	return nil
}

func (c *ConsoleTrust) readConnectorOrganization(ctx context.Context) (string, error) {
	said, err := c.readFile(ctx, "read the organization this box's connector serves", ConnectorConfig)
	if err != nil || said == "" {
		return "", err
	}
	var config struct {
		OrganizationID string `json:"organizationId"`
	}
	if err := json.Unmarshal([]byte(said), &config); err != nil {
		return "", refusal.Refuse(refusal.CodeInvalid, "%s is not a connector config: %s\nRun `ocel connector add` again", ConnectorConfig, err)
	}
	return config.OrganizationID, nil
}

func (c *ConsoleTrust) readFile(ctx context.Context, what, path string) (string, error) {
	quoted := shell.Quote(path)
	said, err := c.host.Run(ctx, ssh.RootWhenAble, what, "if [ -f "+quoted+" ]; then cat "+quoted+"; else echo "+trustAbsent+"; fi", nil)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(said) == trustAbsent {
		return "", nil
	}
	return said, nil
}

func RefuseConflictingTrust(tier environment.Tier, trust provider.WebIdentityTrust, read TrustReading) error {
	if read.ConnectorOrganization != "" && read.ConnectorOrganization != trust.OrganizationID {
		return refusal.Refuse(refusal.CodeInvalid,
			"this box's connector serves organization %s, and the console asks %s to trust organization %s\nRun `ocel connector add` from organization %s, or bootstrap logged in to organization %s",
			read.ConnectorOrganization, tier, trust.OrganizationID, trust.OrganizationID, read.ConnectorOrganization)
	}
	sibling := read.Sibling
	if sibling == nil || (sibling.Issuer == trust.Issuer && sibling.Audience == trust.Audience && sibling.TargetID == trust.TargetID && sibling.OrganizationID == trust.OrganizationID) {
		return nil
	}
	return refusal.Refuse(refusal.CodeInvalid,
		"%s trusts console target %s of organization %s, and %s would trust target %s of organization %s; one box answers to one target\nBootstrap both tiers from the same organization, or first run `ocel bootstrap %s --remove %s`",
		tier.Sibling(), sibling.TargetID, sibling.OrganizationID, tier, trust.TargetID, trust.OrganizationID, tier.Sibling(), provider.ConsoleTrustPart)
}

func (c *ConsoleTrust) Apply(ctx context.Context, tier environment.Tier, trust provider.WebIdentityTrust, read TrustReading, progress progress.Log) error {
	if err := c.host.InstallChanged(ctx, ConsoleTrustItems(tier, trust), CurrentIn(read.Observed), progress); err != nil {
		return err
	}
	if _, err := c.host.Run(ctx, ssh.Root, "check sshd still reads its config", sshdValidation(SSHDDropIn), nil); err != nil {
		var refused refusal.Refusal
		if errors.As(err, &refused) {
			return refusal.Refuse(refusal.CodeInvalid,
				"`sshd -t` on %s refuses its config with %s in it, so the drop-in was taken out again: %s\nFix what sshd names, then run `%s` again",
				c.host.Named(), SSHDDropIn, refused.Message, provider.BootstrapCommand(tier))
		}
		return err
	}
	if _, err := c.host.Run(ctx, ssh.Root, "have sshd trust the box's certificates", sshdReload()+"\n"+sshdTrustCheck(), nil); err != nil {
		var refused refusal.Refusal
		if errors.As(err, &refused) {
			return refusal.Refuse(refusal.CodeNotReady,
				"sshd on %s does not apply %s to %s: %s\nAdd `Include /etc/ssh/sshd_config.d/*.conf` at the top of /etc/ssh/sshd_config, then run `%s` again",
				c.host.Named(), SSHDDropIn, deployUser, refused.Message, provider.BootstrapCommand(tier))
		}
		return err
	}
	if err := c.host.Install(ctx, consoleTrustDigests(tier, trust)); err != nil {
		return err
	}
	return c.restartConnector(ctx, progress, "exchanges the console's credentials for certificates")
}

func sshdValidation(dropIn string) string {
	return "if ! said=$(sshd -t 2>&1); then rm -f " + shell.Quote(dropIn) + "; printf '%s\\n' \"$said\" >&2; exit 1; fi"
}

func sshdReload() string {
	return strings.Join([]string{
		"set -e",
		"if systemctl is-active --quiet ssh; then systemctl reload ssh; elif systemctl is-active --quiet sshd; then systemctl reload sshd; fi",
	}, "\n")
}

func sshdTrustCheck() string {
	applied := "sshd -T -C " + shell.Quote("user="+deployUser+",host=localhost,addr=127.0.0.1")
	return strings.Join([]string{
		"applied=$(" + applied + ")",
		"printf '%s\\n' \"$applied\" | grep -qix " + shell.Quote("trustedusercakeys "+boxapi.AuthorityPublicKey),
		"printf '%s\\n' \"$applied\" | grep -qix " + shell.Quote("authorizedprincipalsfile "+boxapi.AuthorizedPrincipals),
	}, "\n")
}

func (c *ConsoleTrust) restartConnector(ctx context.Context, progress progress.Log, serving string) error {
	unit := shell.Quote(ConnectorUnit)
	said, err := c.host.Run(ctx, ssh.Root, "restart the connector so it serves the box's roles",
		"if systemctl cat "+unit+" >/dev/null 2>&1; then systemctl try-restart "+unit+"; echo "+connectorInstalled+"; fi", nil)
	if err != nil {
		return err
	}
	if strings.TrimSpace(said) != connectorInstalled && serving != "" {
		progress.Warn("This box runs no connector, so nothing " + serving + " yet: run `ocel connector add`")
	}
	return nil
}

func (c *ConsoleTrust) Remove(ctx context.Context, tier environment.Tier, progress progress.Log) error {
	trusted := shell.Quote(boxapi.ConsoleTrustPath(tier)) + " " + shell.Quote(boxapi.ConsoleTrustDigestsPath(tier))
	if _, err := c.host.Run(ctx, ssh.Root, "forget the console "+string(tier)+" trusts", "rm -f "+trusted, nil); err != nil {
		return err
	}
	progress.Say("Removed " + machine.Phrase(machine.KindFile, boxapi.ConsoleTrustPath(tier)))
	sibling := shell.Quote(boxapi.ConsoleTrustPath(tier.Sibling()))
	said, err := c.host.Run(ctx, ssh.Root, "take the certificate authority off this box unless "+string(tier.Sibling())+" still trusts the console",
		"if [ -f "+sibling+" ]; then echo kept; exit 0; fi\n"+authorityRemoval()+"\n"+sshdReload(), nil)
	if err != nil {
		return err
	}
	if strings.TrimSpace(said) != "kept" {
		progress.Say("Removed the box's ssh certificate authority, " + SSHDDropIn + " and " + boxapi.AuthorityDir)
	}
	return c.restartConnector(ctx, progress, "")
}

func authorityRemoval() string {
	return strings.Join([]string{
		"rm -f " + shell.Quote(SSHDDropIn) + " " + shell.Quote(sudoersSign),
		"rm -rf " + shell.Quote(boxapi.AuthorityDir),
	}, "\n")
}
