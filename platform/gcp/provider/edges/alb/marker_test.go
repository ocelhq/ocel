package alb

import (
	"testing"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func TestEveryResponseTheLoadBalancerSendsNamesItAsTheEdge(t *testing.T) {
	t.Parallel()

	seen, err := declared(frontProgram(frontSpec{Names: frontNames(edge.ClassProduction)}))
	if err != nil {
		t.Fatalf("the frontend program = %v", err)
	}
	action, _ := seen["ocel-alb-production-routes"].Args["headerAction"].(map[string]any)
	added, _ := action["responseHeadersToAdds"].([]any)
	for _, header := range added {
		fields, _ := header.(map[string]any)
		if fields["headerName"] == edge.HeaderEdge && fields["headerValue"] == string(Kind) && fields["replace"] == true {
			return
		}
	}
	t.Errorf("the url map adds %v to its responses, want %s: %s, the marker a liveness probe reads to know which edge answers a hostname, so no hostname on this edge is ever cut over", added, edge.HeaderEdge, Kind)
}
