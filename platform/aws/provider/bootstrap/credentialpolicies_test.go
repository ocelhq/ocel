package bootstrap

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode"

	"github.com/ocelhq/ocel/platform/aws/provider/iam"
)

type printedPolicy struct {
	PolicyName     string          `json:"PolicyName"`
	PolicyDocument json.RawMessage `json:"PolicyDocument"`
}

func printedPolicies(t *testing.T, document string) []printedPolicy {
	t.Helper()
	var policies []printedPolicy
	if err := json.Unmarshal([]byte(document), &policies); err != nil {
		t.Fatalf("the printed credential is not a list of managed policies: %v\n%s", err, document)
	}
	return policies
}

func iamCharacters(raw []byte) int {
	n := 0
	for _, r := range string(raw) {
		if !unicode.IsSpace(r) {
			n++
		}
	}
	return n
}

func TestEveryCredentialFitsTheManagedPoliciesIAMAccepts(t *testing.T) {
	t.Parallel()

	for name, document := range bothCredentials(t) {
		policies := printedPolicies(t, document)
		if len(policies) == 0 {
			t.Errorf("%s prints no policy", name)
			continue
		}
		if len(policies) > iam.ManagedPoliciesPerPrincipal {
			t.Errorf("%s needs %d managed policies, and IAM attaches %d to a role or user by default", name, len(policies), iam.ManagedPoliciesPerPrincipal)
		}
		seen := map[string]bool{}
		for _, policy := range policies {
			if size := iamCharacters(policy.PolicyDocument); size > iam.ManagedPolicyChars {
				t.Errorf("%s policy %s is %d characters, and IAM refuses a managed policy over %d", name, policy.PolicyName, size, iam.ManagedPolicyChars)
			}
			if policy.PolicyName == "" || seen[policy.PolicyName] {
				t.Errorf("%s names a policy %q, want every policy named apart", name, policy.PolicyName)
			}
			seen[policy.PolicyName] = true
			if !strings.HasPrefix(policy.PolicyName, string(defaultNamespace)+"-") {
				t.Errorf("%s names a policy %q outside the namespace", name, policy.PolicyName)
			}
		}
	}
}

func TestSplittingACredentialKeepsEveryStatement(t *testing.T) {
	t.Parallel()

	grants := deployGrants(defaultNamespace.ScopedARNs("production"), "")
	whole, err := json.Marshal(map[string]any{"Version": "2012-10-17", "Statement": iam.Statements(grants)})
	if err != nil {
		t.Fatal(err)
	}
	_, deployDoc := renderedCredentials(t)
	if got, want := grantsOf(t, deployDoc), grantsOf(t, string(whole)); len(got) != len(want) {
		t.Errorf("the split credential grants %d action-resource pairs, want the %d of the whole", len(got), len(want))
	} else {
		for g := range want {
			if !got[g] {
				t.Errorf("the split credential lost %+v", g)
			}
		}
	}
}
