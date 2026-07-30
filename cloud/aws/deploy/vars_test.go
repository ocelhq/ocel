package deploy

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

const (
	productionVarsKeyARN = "arn:aws:kms:us-east-1:1234:key/prod-key"
	previewVarsKeyARN    = "arn:aws:kms:us-east-1:1234:key/preview-key"
)

// TestVarsDecryptPolicy_GrantsOneKeyAndOneAction proves a function execution
// role can decrypt under its own substrate's class key and nothing else: a
// wildcard resource, or the other class's key, would hand preview compute
// production ciphertext.
func TestVarsDecryptPolicy_GrantsOneKeyAndOneAction(t *testing.T) {
	raw, err := varsDecryptPolicy(productionVarsKeyARN)
	if err != nil {
		t.Fatalf("varsDecryptPolicy: %v", err)
	}

	var doc struct {
		Statement []struct {
			Effect   string   `json:"Effect"`
			Action   []string `json:"Action"`
			Resource string   `json:"Resource"`
		}
	}
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatalf("policy is not valid JSON: %v", err)
	}
	if len(doc.Statement) != 1 {
		t.Fatalf("got %d statements, want exactly the decrypt grant", len(doc.Statement))
	}
	st := doc.Statement[0]
	if st.Effect != "Allow" {
		t.Errorf("Effect = %q, want Allow", st.Effect)
	}
	if want := []string{"kms:Decrypt"}; !slices.Equal(st.Action, want) {
		t.Errorf("Action = %v, want %v", st.Action, want)
	}
	if st.Resource != productionVarsKeyARN {
		t.Errorf("Resource = %q, want the substrate's own key ARN", st.Resource)
	}
	if strings.Contains(raw, previewVarsKeyARN) {
		t.Errorf("policy = %s, must not reach another class's key", raw)
	}
}

// TestAppExecutionRole_CarriesTheSubstratesVarsKey proves the key a role
// decrypts under is the one the substrate this deploy resolved, not a name the
// deploy path derives: a production deploy can only ever render the production
// key into a role, because that is the only key it was handed.
func TestAppExecutionRole_CarriesTheSubstratesVarsKey(t *testing.T) {
	caches := map[string]*isrConfig{"web": {Prefix: "prod/proj/web/WEB1"}}

	role := appExecutionRole(Config{VarsKeyARN: productionVarsKeyARN}, "web", caches)
	if role.VarsKeyARN != productionVarsKeyARN {
		t.Errorf("VarsKeyARN = %q, want the substrate's own key", role.VarsKeyARN)
	}
	if role.Cache != caches["web"] {
		t.Errorf("Cache = %+v, want the app's own cache", role.Cache)
	}

	preview := appExecutionRole(Config{VarsKeyARN: previewVarsKeyARN}, "api", caches)
	if preview.VarsKeyARN != previewVarsKeyARN {
		t.Errorf("VarsKeyARN = %q, want the preview substrate's key", preview.VarsKeyARN)
	}
	if preview.Cache != nil {
		t.Errorf("Cache = %+v, want none for an app that keeps no cache", preview.Cache)
	}
}
