package alb

import "github.com/ocelhq/ocel/pkg/environment"

func URLMapPath(namespace, project string, tier environment.Tier) string {
	return "projects/" + project + "/global/urlMaps/" + loadBalancerNames(namespace, tier).URLMap
}
