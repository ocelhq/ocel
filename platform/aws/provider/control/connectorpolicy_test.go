package control

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/aws/provider/iam"
)

const awsServices = "github.com/aws/aws-sdk-go-v2/service/"

func actionsOf(t *testing.T, service, operation string) []string {
	t.Helper()
	actions, known := operationActions[service+":"+operation]
	if !known {
		t.Errorf("the control assembly calls %s:%s, and operationActions names no IAM action for it: add the actions the Service Authorization Reference maps it to", service, operation)
	}
	return actions
}

func actionsTheControlAssemblyLinks(t *testing.T) []string {
	t.Helper()
	out, err := exec.Command("go", "list", "-deps", "-json", ".").Output()
	if err != nil {
		t.Fatalf("go list -deps: %v", err)
	}
	var linked []string
	decoder := json.NewDecoder(strings.NewReader(string(out)))
	for decoder.More() {
		var pkg struct {
			ImportPath string
			Dir        string
			GoFiles    []string
		}
		if err := decoder.Decode(&pkg); err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(pkg.ImportPath, "github.com/ocelhq/") {
			continue
		}
		for _, file := range pkg.GoFiles {
			linked = append(linked, actionsCalledIn(t, filepath.Join(pkg.Dir, file))...)
		}
	}
	slices.Sort(linked)
	return slices.Compact(linked)
}

func actionsCalledIn(t *testing.T, file string) []string {
	t.Helper()
	parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	services := map[string]string{}
	for _, spec := range parsed.Imports {
		imported, err := strconv.Unquote(spec.Path.Value)
		if err != nil || !strings.HasPrefix(imported, awsServices) {
			continue
		}
		service := strings.TrimPrefix(imported, awsServices)
		if strings.Contains(service, "/") {
			continue
		}
		name := service
		if spec.Name != nil {
			name = spec.Name.Name
		}
		services[name] = service
	}
	var actions []string
	ast.Inspect(parsed, func(node ast.Node) bool {
		literal, ok := node.(*ast.CompositeLit)
		if !ok {
			return true
		}
		selector, ok := literal.Type.(*ast.SelectorExpr)
		if !ok || !strings.HasSuffix(selector.Sel.Name, "Input") {
			return true
		}
		qualifier, ok := selector.X.(*ast.Ident)
		if !ok {
			return true
		}
		if service, isService := services[qualifier.Name]; isService {
			actions = append(actions, actionsOf(t, service, strings.TrimSuffix(selector.Sel.Name, "Input"))...)
		}
		return true
	})
	return actions
}

func grantsByTheDeployCredential(action string) bool {
	for _, tier := range []environment.Tier{environment.TierProduction, environment.TierPreview} {
		for _, statement := range deployStatements(scopeOf(defaultNamespace, tier, "")) {
			if slices.ContainsFunc(statement.Actions, func(pattern string) bool { return matchesAction(pattern, action) }) {
				return true
			}
		}
	}
	return false
}

func TestTheConnectorIsGrantedEveryActionTheControlAssemblyCallsThatADeployMay(t *testing.T) {
	t.Parallel()

	var served, unserved []string
	for _, action := range actionsTheControlAssemblyLinks(t) {
		if grantsByTheDeployCredential(action) {
			served = append(served, action)
		} else {
			unserved = append(unserved, action)
		}
	}
	if !slices.Equal(servedActions, served) {
		t.Errorf("servedActions = %q,\nwant %q: every call the control assembly links that the deploy credential grants", servedActions, served)
	}
	if !slices.Equal(actionsTheDeployCredentialWithholds, unserved) {
		t.Errorf("actionsTheDeployCredentialWithholds = %q,\nwant %q: what the control assembly links that no deploy credential grants", actionsTheDeployCredentialWithholds, unserved)
	}
	granted := map[string]bool{}
	for _, statement := range ConnectorStatements(defaultNamespace, "") {
		for _, action := range statement.Actions {
			granted[action] = true
		}
	}
	for _, action := range served {
		if !granted[action] {
			t.Errorf("the connector is not granted %s, which the control assembly calls", action)
		}
	}
}

func TestTheConnectorsControlPoliciesFitBesideItsExecutionPolicyOnOneRole(t *testing.T) {
	t.Parallel()

	policies, err := ConnectorPolicies(defaultNamespace, "arn:aws:kms:us-east-1:123456789012:key/k")
	if err != nil {
		t.Fatalf("ConnectorPolicies() = %v", err)
	}
	if len(policies)+1 > iam.ManagedPoliciesPerPrincipal {
		t.Errorf("the connector needs %d control policies, and with its execution policy that is over the %d IAM attaches to one role", len(policies), iam.ManagedPoliciesPerPrincipal)
	}
}
