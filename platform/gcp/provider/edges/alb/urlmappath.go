package alb

import "github.com/ocelhq/ocel/pkg/environment"

func URLMapPath(project string, tier environment.Tier) string {
	return "projects/" + project + "/global/urlMaps/" + loadBalancerNames(tier, false).URLMap
}
