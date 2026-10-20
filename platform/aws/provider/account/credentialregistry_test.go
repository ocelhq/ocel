package account

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
)

func TestACredentialReachesTheImageRepositoriesOfOnlyItsOwnNamespace(t *testing.T) {
	repositoryOf := func(ns Namespace) string {
		return "arn:aws:ecr:us-east-1:111122223333:repository/" + string(ns) + "/shop.web"
	}
	for _, pair := range [][2]Namespace{{"ocel", "other"}, {"other", "ocel"}} {
		own, other := pair[0], pair[1]
		document, err := DeployCredentialPermissions(own, environment.TierProduction, "")
		if err != nil {
			t.Fatalf("render the deploy credential of namespace %s: %v", own, err)
		}
		for _, action := range []string{"ecr:PutImage", "ecr:BatchDeleteImage", "ecr:DeleteRepository"} {
			if !reaches(t, document, action, repositoryOf(own)) {
				t.Errorf("the deploy credential of namespace %s cannot %s on its own repository %s", own, action, repositoryOf(own))
			}
			if reaches(t, document, action, repositoryOf(other)) {
				t.Errorf("the deploy credential of namespace %s can %s on %s, namespace %s's repository", own, action, repositoryOf(other), other)
			}
		}
		encoded, err := json.Marshal(appBoundaryResource(own, environment.TierProduction, "").Properties["PolicyDocument"])
		if err != nil {
			t.Fatalf("encode the app boundary: %v", err)
		}
		if got := string(encoded); !strings.Contains(got, "repository/"+string(own)+"/*") || strings.Contains(got, "repository/"+string(other)+"/*") {
			t.Errorf("the %s app boundary admits ECR pulls on %s, want its own namespace's repositories alone", own, got)
		}
	}
}
