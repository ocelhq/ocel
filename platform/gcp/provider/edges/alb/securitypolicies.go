package alb

import (
	"slices"

	"github.com/ocelhq/ocel/pkg/provider/transform"
)

var securityPolicyFields = []string{"securityPolicy", "edgeSecurityPolicy"}

func SecurityPolicies(result transform.Result) []string {
	var named []string
	for _, surface := range []string{surfaceBackendService, surfaceNotFoundBackend, surfaceAssetsBackend} {
		for _, field := range securityPolicyFields {
			policy, set := result.Patches[surface][field].(string)
			if set && policy != "" && !slices.Contains(named, policy) {
				named = append(named, policy)
			}
		}
	}
	return named
}
