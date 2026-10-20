package account

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/aws/provider/registry"
)

func repositoryARNOf(ns Namespace, tier environment.Tier) string {
	return "arn:aws:ecr:us-east-1:111122223333:repository/" + registry.RepositoryPrefix(string(ns), tier) + "/shop.web"
}

func appBoundaryDocument(t *testing.T, ns Namespace, tier environment.Tier) string {
	t.Helper()
	encoded, err := json.Marshal(appBoundaryResource(ns, tier, "").Properties["PolicyDocument"])
	if err != nil {
		t.Fatalf("encode the app boundary: %v", err)
	}
	return string(encoded)
}

func TestACredentialReachesTheImageRepositoriesOfOnlyItsOwnNamespace(t *testing.T) {
	for _, pair := range [][2]Namespace{{"ocel", "other"}, {"other", "ocel"}} {
		own, other := pair[0], pair[1]
		document, err := DeployCredentialPermissions(own, environment.TierProduction, "")
		if err != nil {
			t.Fatalf("render the deploy credential of namespace %s: %v", own, err)
		}
		for _, action := range []string{"ecr:PutImage", "ecr:BatchDeleteImage", "ecr:DeleteRepository"} {
			if !reaches(t, document, action, repositoryARNOf(own, environment.TierProduction)) {
				t.Errorf("the deploy credential of namespace %s cannot %s on its own repository %s", own, action, repositoryARNOf(own, environment.TierProduction))
			}
			if reaches(t, document, action, repositoryARNOf(other, environment.TierProduction)) {
				t.Errorf("the deploy credential of namespace %s can %s on %s, namespace %s's repository", own, action, repositoryARNOf(other, environment.TierProduction), other)
			}
		}
		if got := appBoundaryDocument(t, own, environment.TierProduction); !strings.Contains(got, "repository/"+string(own)+"/") || strings.Contains(got, "repository/"+string(other)+"/") {
			t.Errorf("the %s app boundary admits ECR pulls on %s, want its own namespace's repositories alone", own, got)
		}
	}
}

func TestACredentialAndTheAppBoundaryReachTheImageRepositoriesOfOnlyTheirOwnTier(t *testing.T) {
	for _, tier := range bothTiers {
		sibling := tier.Sibling()
		for purpose, document := range credentialsOfTier(t, tier) {
			for _, action := range []string{"ecr:PutImage", "ecr:BatchGetImage", "ecr:BatchDeleteImage", "ecr:DeleteRepository"} {
				if !reaches(t, document, action, repositoryARNOf(defaultNamespace, tier)) {
					t.Errorf("the %s %s credential cannot %s on its own tier's repository %s", tier, purpose, action, repositoryARNOf(defaultNamespace, tier))
				}
				if reaches(t, document, action, repositoryARNOf(defaultNamespace, sibling)) {
					t.Errorf("the %s %s credential can %s on %s, a repository of the %s tier", tier, purpose, action, repositoryARNOf(defaultNamespace, sibling), sibling)
				}
			}
		}
		boundary := appBoundaryDocument(t, defaultNamespace, tier)
		if own := "repository/" + registry.RepositoryPrefix(string(defaultNamespace), tier) + "/*"; !strings.Contains(boundary, own) {
			t.Errorf("the %s app boundary admits no ECR pull on %s, so its containers cannot pull their own images", tier, own)
		}
		if other := "repository/" + registry.RepositoryPrefix(string(defaultNamespace), sibling) + "/"; strings.Contains(boundary, other) {
			t.Errorf("the %s app boundary admits ECR pulls under %s, the %s tier's repositories", tier, other, sibling)
		}
	}
}
