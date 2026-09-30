package traefik

import "github.com/ocelhq/ocel/platform/vps/provider/switchboard"

const labelled = `{{json .Name}} {{json .Config.Labels}}`

const serviceLabelled = `{{json .Spec.Name}} {{json .Spec.Labels}}`

func containerLabels() []string {
	return []string{"sh", "-c", "docker ps --quiet | xargs -r docker inspect --type container --format '" + labelled + "'"}
}

const networkFact = "network="

func switchboardNetworks() []string {
	return []string{"docker", "inspect", "--type", "container", "--format",
		"{{range $name, $_ := .NetworkSettings.Networks}}" + networkFact + "{{$name}} {{end}}",
		switchboard.Name}
}

func coolifyProxyImage() []string {
	return []string{"sh", "-c", "docker inspect --type container --format '{{.Config.Image}}' " + coolifyProxy + " 2>/dev/null || true"}
}

func serviceLabels() []string {
	return []string{"sh", "-c",
		`if [ "$(docker info --format '{{.Swarm.ControlAvailable}}' 2>/dev/null)" = true ]; then ` +
			"docker service ls --quiet | xargs -r docker service inspect --format '" + serviceLabelled + "'; fi"}
}
