package googletest

import runv2 "google.golang.org/api/run/v2"

func EnvOf(container *runv2.GoogleCloudRunV2Container) map[string]string {
	env := map[string]string{}
	for _, entry := range container.Env {
		env[entry.Name] = entry.Value
	}
	return env
}
