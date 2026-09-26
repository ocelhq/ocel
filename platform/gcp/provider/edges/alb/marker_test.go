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
		held, _ := header.(map[string]any)
		if held["headerName"] == edge.HeaderEdge && held["headerValue"] == string(Kind) && held["replace"] == true {
			return
		}
	}
	t.Errorf("the url map adds %v to its responses, want %s: %s, the marker a settle reads to know which edge answers a hostname, so no hostname on this edge is ever settled", added, edge.HeaderEdge, Kind)
}
