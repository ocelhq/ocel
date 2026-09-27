package traefik

const labelled = `{{json .Name}} {{json .Config.Labels}}`

const serviceLabelled = `{{json .Spec.Name}} {{json .Spec.Labels}}`

func containerLabels() []string {
	return []string{"sh", "-c", "docker ps --quiet | xargs -r docker inspect --type container --format '" + labelled + "'"}
}

func serviceLabels() []string {
	return []string{"sh", "-c",
		`if [ "$(docker info --format '{{.Swarm.ControlAvailable}}' 2>/dev/null)" = true ]; then ` +
			"docker service ls --quiet | xargs -r docker service inspect --format '" + serviceLabelled + "'; fi"}
}
