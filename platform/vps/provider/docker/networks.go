package docker

import (
	"context"
	"strings"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/shell"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

const (
	poolExhausted = "address pool"
	MembersFormat = `{{range .Containers}}{{.Name}}{{"\n"}}{{end}}`
)

func networkLabels(tier environment.Tier, project string) []string {
	return []string{
		"--label", LabelTier + "=" + string(tier),
		"--label", LabelProject + "=" + naming.Sanitize(project),
	}
}

func CreatingNetwork(tier environment.Tier, project string) string {
	network := shell.Quote(boxapi.AppNetwork(tier, project))
	create := "docker network create " + shell.Words(networkLabels(tier, project)) + " " + network
	return "set -e\n" +
		"if ! docker network inspect " + network + " >/dev/null 2>&1; then\n" +
		"if ! said=$(" + create + " 2>&1 >/dev/null) && ! docker network inspect " + network + " >/dev/null 2>&1; then\n" +
		"printf '%s\\n' \"$said\" >&2\n" +
		"exit 1\n" +
		"fi\n" +
		"fi"
}

func JoiningNetwork(tier environment.Tier, project string) string {
	network := shell.Quote(boxapi.AppNetwork(tier, project))
	return CreatingNetwork(tier, project) + "\n" +
		"if ! docker network connect " + network + " " + shell.Quote(boxapi.Switchboard) + " >/dev/null 2>&1 && " +
		"! docker network inspect --format " + shell.Quote(MembersFormat) + " " + network + " | grep -qx " + shell.Quote(boxapi.Switchboard) + "; then\n" +
		"printf '%s\\n' " + shell.Quote(boxapi.Switchboard+" could not join "+boxapi.AppNetwork(tier, project)) + " >&2\n" +
		"exit 1\n" +
		"fi"
}

func ForgettingNetwork(tier environment.Tier, project string) string {
	network := shell.Quote(boxapi.AppNetwork(tier, project))
	return "if docker network inspect " + network + " >/dev/null 2>&1; then\n" +
		"if docker network inspect --format " + shell.Quote(MembersFormat) + " " + network +
		" | grep -qvx " + shell.Quote(boxapi.Switchboard) + "; then printf '%s\\n' " + shell.Quote(NetworkInUse) + "; exit 0; fi\n" +
		"docker network disconnect --force " + network + " " + shell.Quote(boxapi.Switchboard) + " >/dev/null 2>&1 || true\n" +
		"docker network rm " + network + " >/dev/null\n" +
		"fi"
}

func (e *Engine) Join(ctx context.Context, who string, tier environment.Tier, project string) error {
	_, said, err := e.box.Attempt(ctx, ssh.Docker, "put "+who+" on "+boxapi.AppNetwork(tier, project), JoiningNetwork(tier, project), nil)
	if err != nil && strings.Contains(said, poolExhausted) {
		return refusal.Refuse(refusal.CodeNotReady,
			"%s has no subnet left for %s: %s\n"+
				"Add `\"default-address-pools\": [{\"base\": \"10.200.0.0/16\", \"size\": 24}]` to /etc/docker/daemon.json and restart docker",
			e.box.Named(), boxapi.AppNetwork(tier, project), said)
	}
	return err
}

func (e *Engine) ForgetNetwork(ctx context.Context, tier environment.Tier, project string) error {
	_, err := e.box.Run(ctx, ssh.Docker, "take "+boxapi.AppNetwork(tier, project)+" down", ForgettingNetwork(tier, project), nil)
	return err
}

const (
	NetworkFact   = "network=present"
	NetworkInUse  = "network=in-use"
	NetworkJoined = "joined"
	NetworkLeft   = "left"
)

func EnsuringNetwork(name string) string {
	return "docker network inspect " + shell.Quote(name) + " >/dev/null 2>&1 || " +
		"docker network create " + shell.Quote(name) + " >/dev/null"
}

func JoinedFact(joined Network) string { return "network:" + joined.Name + "=" }

func rejoining(name string) string {
	return "for net in $(docker network ls --quiet --filter " + shell.Quote("label="+LabelTier) + "); do\n" +
		"docker network connect \"$net\" " + shell.Quote(name) + " >/dev/null\n" +
		"done\n"
}

type Network struct {
	Name          string
	Option        string
	CreatedByOcel bool
}
