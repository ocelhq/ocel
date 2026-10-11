package deploy

import (
	"encoding/json"
	"reflect"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/provider"
)

var loginGate = edge.PreviewGate{
	Key:           "gate-key",
	PasswordMAC:   "password-mac",
	BypassSecrets: []string{"bypass-one"},
	AllowOptions:  []string{"/api/"},
}

func decodedGate(t *testing.T, env map[string]string) edge.PreviewGate {
	t.Helper()
	encoded, handed := env[edge.PreviewGateVar]
	if !handed {
		t.Fatalf("env has no %s, want the gate the login is checked against", edge.PreviewGateVar)
	}
	var gate edge.PreviewGate
	if err := json.Unmarshal([]byte(encoded), &gate); err != nil {
		t.Fatalf("%s = %q: %v", edge.PreviewGateVar, encoded, err)
	}
	return gate
}

func TestAPreviewRootFunctionIsHandedTheGateItAnswersTheLoginWith(t *testing.T) {
	t.Parallel()

	cfg, spec := appStackSpec(t)
	cfg.OriginSecret = testOriginSecret
	spec.App.Guard = &provider.OriginGuard{RootFunction: "fn--web--entry"}
	spec.App.PreviewGate = &loginGate

	work, err := releasing(t, cfg).appWork(spec, nil)
	if err != nil {
		t.Fatalf("appWork() = %v", err)
	}

	if got := decodedGate(t, work.functions.Guard.rootFunctionEnv(nil)); !reflect.DeepEqual(got, loginGate) {
		t.Errorf("root function gate = %+v, want %+v", got, loginGate)
	}
	if _, handed := work.functions.Env[edge.PreviewGateVar]; handed {
		t.Errorf("every function is handed %s, want only the root function the edge sends the login to", edge.PreviewGateVar)
	}
}

func TestAPreviewGateWithNoRootFunctionToAnswerTheLoginIsRefused(t *testing.T) {
	t.Parallel()

	cfg, spec := appStackSpec(t)
	spec.App.PreviewGate = &loginGate

	if _, err := releasing(t, cfg).appWork(spec, nil); err == nil {
		t.Fatal("appWork() = nil, want it refused: the login would reach the app")
	}
}

func TestAPreviewContainerIsHandedTheGateItAnswersTheLoginWith(t *testing.T) {
	t.Parallel()

	cfg, spec := containerStackSpec(t)
	spec.App.PreviewGate = &loginGate

	work, err := releasing(t, cfg).containerWork(spec, fixtureContainerInfra())
	if err != nil {
		t.Fatalf("containerWork() = %v", err)
	}

	if got := decodedGate(t, work.env); !reflect.DeepEqual(got, loginGate) {
		t.Errorf("container gate = %+v, want %+v", got, loginGate)
	}
}

func TestThePreviewGateKeysAreKeptOutOfEveryStackOutput(t *testing.T) {
	t.Parallel()

	cfg, spec := appStackSpec(t)
	spec.App.PreviewGate = &loginGate

	secrets := releasing(t, cfg).Secrets(spec)

	for _, key := range []string{loginGate.Key, loginGate.PasswordMAC, loginGate.BypassSecrets[0]} {
		if !slices.Contains(secrets, key) {
			t.Errorf("Secrets() = %q, want it to mask %q", secrets, key)
		}
	}
}
