package ocel_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"strings"
	"testing"
	"time"

	ocel "github.com/ocelhq/ocel/sdk"
)

type cell struct {
	Key    string `json:"key"`
	Folder string `json:"folder"`
	Value  string `json:"value"`
}

func variablesServer(t *testing.T, cells []cell, seen *[]map[string]any) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("unmarshal %q: %v", raw, err)
		}
		body["__path"] = r.URL.Path
		*seen = append(*seen, body)
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/DeclareEnv") {
			_ = json.NewEncoder(w).Encode(map[string]any{"cells": cells})
			return
		}
		_, _ = w.Write([]byte("{}"))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func discover(t *testing.T, cells []cell) *[]map[string]any {
	t.Helper()
	var seen []map[string]any
	srv := variablesServer(t, cells, &seen)
	t.Setenv("OCEL_PHASE", "discovery")
	t.Setenv("OCEL_DEV_SERVER", srv.URL)
	return &seen
}

func caught(t *testing.T, run func()) (err error) {
	t.Helper()
	defer func() {
		recovered := recover()
		if recovered == nil {
			return
		}
		var ok bool
		if err, ok = recovered.(error); !ok {
			t.Fatalf("panicked with %T %v, want an error", recovered, recovered)
		}
	}()
	run()
	return nil
}

func definitionError(t *testing.T, run func()) *ocel.EnvDefinitionError {
	t.Helper()
	err := caught(t, run)
	var definition *ocel.EnvDefinitionError
	if !errors.As(err, &definition) {
		t.Fatalf("error = %v, want an *EnvDefinitionError", err)
	}
	return definition
}

func valueError(t *testing.T, run func()) *ocel.EnvValueError {
	t.Helper()
	err := caught(t, run)
	var value *ocel.EnvValueError
	if !errors.As(err, &value) {
		t.Fatalf("error = %v, want an *EnvValueError", err)
	}
	return value
}

func byPath(seen []map[string]any, suffix string) map[string]any {
	for _, body := range seen {
		if strings.HasSuffix(body["__path"].(string), suffix) {
			return body
		}
	}
	return nil
}

func TestEnvRejectsAnUnusableKey(t *testing.T) {
	type env struct {
		Name string `ocel:"lower-case"`
	}
	err := definitionError(t, func() { ocel.Env[env]() })
	if !strings.Contains(err.Error(), "'lower-case' is not a usable variable name") {
		t.Errorf("error = %q", err)
	}
}

func TestEnvRejectsAnOcelOwnedNameForABareKeyClass(t *testing.T) {
	type env struct {
		Name string `ocel:"OCEL_THING"`
	}
	err := definitionError(t, func() { ocel.Env[env]() })
	if !strings.Contains(err.Error(), "reserved prefix OCEL_") {
		t.Errorf("error = %q", err)
	}
}

func TestEnvAllowsAnOcelOwnedNameForAClassNeverDeliveredBare(t *testing.T) {
	t.Setenv("OCEL_VAR_OCEL_THING", "v")
	type env struct {
		Name string `ocel:"OCEL_THING,sensitive"`
	}
	if got := ocel.Env[env]().Name; got != "v" {
		t.Errorf("Name = %q, want %q", got, "v")
	}
}

func TestEnvRejectsTheDeploymentURLUnderEveryClass(t *testing.T) {
	for _, class := range []string{"plain", "sensitive", "secret"} {
		t.Run(class, func(t *testing.T) {
			err := caught(t, func() {
				switch class {
				case "plain":
					ocel.Env[struct {
						URL string `ocel:"OCEL_URL"`
					}]()
				case "sensitive":
					ocel.Env[struct {
						URL string `ocel:"OCEL_URL,sensitive"`
					}]()
				case "secret":
					ocel.Env[struct {
						URL ocel.Secret `ocel:"OCEL_URL"`
					}]()
				}
			})
			if err == nil || !strings.Contains(err.Error(), "Read it with DeploymentURL") {
				t.Errorf("error = %v", err)
			}
		})
	}
}

func TestEnvRejectsADefaultOnASecret(t *testing.T) {
	type env struct {
		Key ocel.Secret `ocel:"SIGNING_KEY,default=x"`
	}
	err := definitionError(t, func() { ocel.Env[env]() })
	if !strings.Contains(err.Error(), "A live value must fail loudly") {
		t.Errorf("error = %q", err)
	}
}

func TestEnvRejectsAnOptionalSecret(t *testing.T) {
	type env struct {
		Key *ocel.Secret `ocel:"SIGNING_KEY"`
	}
	err := definitionError(t, func() { ocel.Env[env]() })
	if !strings.Contains(err.Error(), "A live value must fail loudly") {
		t.Errorf("error = %q", err)
	}
}

func TestEnvRejectsTheSecretClassAsATagOption(t *testing.T) {
	type env struct {
		Key string `ocel:"SIGNING_KEY,secret"`
	}
	err := definitionError(t, func() { ocel.Env[env]() })
	if !strings.Contains(err.Error(), "make it an ocel.Secret and drop the option") {
		t.Errorf("error = %q", err)
	}
}

func TestEnvRejectsAClassOptionOnASecret(t *testing.T) {
	type env struct {
		Key ocel.Secret `ocel:"SIGNING_KEY,sensitive"`
	}
	err := definitionError(t, func() { ocel.Env[env]() })
	if !strings.Contains(err.Error(), "A Secret field is always the secret class") {
		t.Errorf("error = %q", err)
	}
}

func TestEnvDeclaresTheClassTheTagNames(t *testing.T) {
	seen := discover(t, []cell{{Key: "P", Value: "v"}, {Key: "S", Value: "v"}, {Key: "E", Value: "v"}, {Key: "X"}})

	ocel.Env[struct {
		Plain     string      `ocel:"P"`
		Sensitive string      `ocel:"S,sensitive"`
		Explicit  string      `ocel:"E,plain"`
		Secret    ocel.Secret `ocel:"X"`
	}]()

	declared := byPath(*seen, "/DeclareEnv")
	var classes []string
	for _, d := range declared["definitions"].([]any) {
		classes = append(classes, d.(map[string]any)["class"].(string))
	}
	want := []string{"VARIABLE_CLASS_PLAIN", "VARIABLE_CLASS_SENSITIVE", "VARIABLE_CLASS_PLAIN", "VARIABLE_CLASS_SECRET"}
	if !equal(classes, want) {
		t.Errorf("classes = %v, want %v", classes, want)
	}
}

func TestEnvRejectsAnUnknownTagOption(t *testing.T) {
	type env struct {
		Key string `ocel:"KEY,required"`
	}
	err := definitionError(t, func() { ocel.Env[env]() })
	if !strings.Contains(err.Error(), "unknown tag option 'required'") {
		t.Errorf("error = %q", err)
	}
}

func TestEnvRejectsAnUnusableFolderScope(t *testing.T) {
	for _, tc := range []struct{ folders, want string }{
		{"apps/web", "must start with '/'"},
		{"/", "'/' is the project root"},
		{"/apps/web/", "must not end with '/'"},
		{"/apps//web", "no empty segments"},
		{"/apps/web;/apps/web", "is named twice"},
	} {
		err := caught(t, func() {
			switch tc.folders {
			case "apps/web":
				ocel.Env[struct {
					K string `ocel:"KEY,folders=apps/web"`
				}]()
			case "/":
				ocel.Env[struct {
					K string `ocel:"KEY,folders=/"`
				}]()
			case "/apps/web/":
				ocel.Env[struct {
					K string `ocel:"KEY,folders=/apps/web/"`
				}]()
			case "/apps//web":
				ocel.Env[struct {
					K string `ocel:"KEY,folders=/apps//web"`
				}]()
			case "/apps/web;/apps/web":
				ocel.Env[struct {
					K string `ocel:"KEY,folders=/apps/web;/apps/web"`
				}]()
			}
		})
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("folders=%s: error = %v, want %q", tc.folders, err, tc.want)
		}
	}
}

func TestEnvRejectsAFieldNothingParsesInto(t *testing.T) {
	type env struct {
		Key []string `ocel:"KEY"`
	}
	err := definitionError(t, func() { ocel.Env[env]() })
	if !strings.Contains(err.Error(), "is read into a []string, which nothing parses a value into") {
		t.Errorf("error = %q", err)
	}
}

func TestEnvRejectsADefaultItsOwnTypeRejects(t *testing.T) {
	type env struct {
		Port int `ocel:"PORT,default=eighty"`
	}
	err := definitionError(t, func() { ocel.Env[env]() })
	if !strings.Contains(err.Error(), "has a default its own type rejects") {
		t.Errorf("error = %q", err)
	}
}

func TestEnvRejectsAnUntaggedField(t *testing.T) {
	type env struct {
		Name string
	}
	err := definitionError(t, func() { ocel.Env[env]() })
	if !strings.Contains(err.Error(), "field Name carries no `ocel` tag") {
		t.Errorf("error = %q", err)
	}
}

func TestEnvRejectsAKeyDeclaredTwiceInOneStruct(t *testing.T) {
	type env struct {
		A string `ocel:"TWICE"`
		B string `ocel:"TWICE"`
	}
	err := definitionError(t, func() { ocel.Env[env]() })
	if !strings.Contains(err.Error(), "'TWICE' is declared by two fields") {
		t.Errorf("error = %q", err)
	}
}

func TestEnvRejectsANonStruct(t *testing.T) {
	err := definitionError(t, func() { ocel.Env[string]() })
	if !strings.Contains(err.Error(), "Env wants a struct type") {
		t.Errorf("error = %q", err)
	}
}

func TestEnvLetsTheFileThatDeclaredAKeyDeclareItAgain(t *testing.T) {
	t.Setenv("OWNED", "v")
	type env struct {
		Owned string `ocel:"OWNED"`
	}
	ocel.Env[env]()
	if err := caught(t, func() { ocel.Env[env]() }); err != nil {
		t.Errorf("second declaration from the same file failed: %v", err)
	}
}

func TestEnvDeclaresEveryVariableWithItsClassAndWhetherItIsRequired(t *testing.T) {
	seen := discover(t, nil)

	type env struct {
		Plain     string        `ocel:"PLAIN"`
		Sensitive string        `ocel:"SENSITIVE,sensitive"`
		Secret    ocel.Secret   `ocel:"SECRET"`
		Defaulted int           `ocel:"DEFAULTED,default=3"`
		Optional  *bool         `ocel:"OPTIONAL"`
		Scoped    time.Duration `ocel:"SCOPED,folders=/apps/web;/apps/api"`
	}
	got := ocel.Env[env]()

	if got != (env{}) {
		t.Errorf("Env() during discovery = %+v, want the zero struct", got)
	}
	declared := byPath(*seen, "/app.resources.v1.ResourceService/DeclareEnv")
	if declared == nil {
		t.Fatalf("no DeclareEnv call among %v", *seen)
	}
	definitions := declared["definitions"].([]any)
	if len(definitions) != 6 {
		t.Fatalf("definitions = %d, want 6", len(definitions))
	}
	want := []map[string]any{
		{"key": "PLAIN", "class": "VARIABLE_CLASS_PLAIN", "required": true},
		{"key": "SENSITIVE", "class": "VARIABLE_CLASS_SENSITIVE", "required": true},
		{"key": "SECRET", "class": "VARIABLE_CLASS_SECRET", "required": true},
		{"key": "DEFAULTED", "class": "VARIABLE_CLASS_PLAIN", "hasSchema": true},
		{"key": "OPTIONAL", "class": "VARIABLE_CLASS_PLAIN", "hasSchema": true},
		{"key": "SCOPED", "class": "VARIABLE_CLASS_PLAIN", "required": true, "hasSchema": true, "folders": []any{"/apps/web", "/apps/api"}},
	}
	for i, w := range want {
		d := definitions[i].(map[string]any)
		for field, value := range w {
			if got := d[field]; !equal(got, value) {
				t.Errorf("definitions[%d].%s = %v, want %v", i, field, got, value)
			}
		}
		if _, ok := d["required"]; ok != (w["required"] == true) {
			t.Errorf("definitions[%d].required present = %v, want %v", i, ok, w["required"] == true)
		}
		if source, _ := d["source"].(string); !strings.Contains(source, "env_test.go:") {
			t.Errorf("definitions[%d].source = %q, want the call site", i, source)
		}
	}
	if byPath(*seen, "/ReportEnvProblems") == nil {
		t.Error("no ReportEnvProblems call, want one for the missing required cells")
	}
}

func equal(got, want any) bool {
	g, _ := json.Marshal(got)
	w, _ := json.Marshal(want)
	return string(g) == string(w)
}

func TestEnvReportsARequiredKeyTheStoreHasNoCellFor(t *testing.T) {
	seen := discover(t, []cell{{Key: "PRESENT", Value: "v"}})

	ocel.Env[struct {
		Present string `ocel:"PRESENT"`
		Missing string `ocel:"MISSING"`
	}]()

	problems := reported(t, *seen)
	if len(problems) != 1 {
		t.Fatalf("problems = %v, want one", problems)
	}
	if p := problems[0]; p["key"] != "MISSING" || p["kind"] != "KIND_MISSING" {
		t.Errorf("problem = %v", p)
	}
}

func reported(t *testing.T, seen []map[string]any) []map[string]any {
	t.Helper()
	body := byPath(seen, "/ReportEnvProblems")
	if body == nil {
		return nil
	}
	var out []map[string]any
	for _, p := range body["problems"].([]any) {
		out = append(out, p.(map[string]any))
	}
	return out
}

func TestEnvReportsAPresentValueItsTypeRejects(t *testing.T) {
	seen := discover(t, []cell{{Key: "PORT", Value: "eighty"}, {Key: "PORT", Folder: "/apps/web", Value: "80"}})

	ocel.Env[struct {
		Port int `ocel:"PORT"`
	}]()

	problems := reported(t, *seen)
	if len(problems) != 1 {
		t.Fatalf("problems = %v, want one", problems)
	}
	if p := problems[0]; p["key"] != "PORT" || p["kind"] != "KIND_INVALID" || p["folder"] != nil || !strings.Contains(p["detail"].(string), "invalid syntax") {
		t.Errorf("problem = %v", p)
	}
}

func TestEnvKeepsAnEncryptedValueOutOfTheProblemItReports(t *testing.T) {
	seen := discover(t, []cell{{Key: "LIMIT", Value: "s3cret"}})

	ocel.Env[struct {
		Limit int `ocel:"LIMIT,sensitive"`
	}]()

	problems := reported(t, *seen)
	if len(problems) != 1 {
		t.Fatalf("problems = %v, want one", problems)
	}
	detail := problems[0]["detail"].(string)
	if strings.Contains(detail, "s3cret") || !strings.Contains(detail, "withheld") {
		t.Errorf("detail = %q", detail)
	}
}

func TestEnvSaysNothingWhenEveryCellSatisfiesItsType(t *testing.T) {
	seen := discover(t, []cell{{Key: "PORT", Value: "80"}, {Key: "NAME", Value: "n"}})

	ocel.Env[struct {
		Port int    `ocel:"PORT"`
		Name string `ocel:"NAME"`
		Opt  *int   `ocel:"OPT"`
		Def  int    `ocel:"DEF,default=1"`
	}]()

	if problems := reported(t, *seen); problems != nil {
		t.Errorf("problems = %v, want none", problems)
	}
}

func TestEnvTakesALiveCellAsPresentWithoutAValueAndNeverChecksIt(t *testing.T) {
	seen := discover(t, []cell{{Key: "KEY"}})

	ocel.Env[struct {
		Key ocel.Secret `ocel:"KEY"`
	}]()

	if problems := reported(t, *seen); problems != nil {
		t.Errorf("problems = %v, want none", problems)
	}
}

type level int

func (l *level) UnmarshalText(text []byte) error {
	switch string(text) {
	case "debug":
		*l = 1
	case "info":
		*l = 2
	default:
		return errors.New("not a level")
	}
	return nil
}

func TestEnvParsesEveryFieldIntoItsType(t *testing.T) {
	t.Setenv("STR", "s")
	t.Setenv("B", "true")
	t.Setenv("I", "-7")
	t.Setenv("U8", "255")
	t.Setenv("UP", "4096")
	t.Setenv("F", "1.5")
	t.Setenv("D", "1m30s")
	t.Setenv("LEVEL", "info")
	t.Setenv("ADDR", "10.0.0.1")
	t.Setenv("OPT", "9")

	type env struct {
		Str   string        `ocel:"STR"`
		B     bool          `ocel:"B"`
		I     int           `ocel:"I"`
		U8    uint8         `ocel:"U8"`
		UP    uintptr       `ocel:"UP"`
		F     float64       `ocel:"F"`
		D     time.Duration `ocel:"D"`
		Level level         `ocel:"LEVEL"`
		Addr  netip.Addr    `ocel:"ADDR"`
		Opt   *int          `ocel:"OPT"`
		Unset *int          `ocel:"UNSET"`
		Def   int           `ocel:"DEF,default=42"`
	}
	got := ocel.Env[env]()

	if got.Str != "s" || !got.B || got.I != -7 || got.U8 != 255 || got.UP != 4096 || got.F != 1.5 || got.D != 90*time.Second || got.Level != 2 || got.Addr != netip.MustParseAddr("10.0.0.1") || got.Opt == nil || *got.Opt != 9 || got.Unset != nil || got.Def != 42 {
		t.Errorf("Env() = %+v", got)
	}
}

func TestEnvPrefersTheNamespacedValueOverABareNameOfTheSameKey(t *testing.T) {
	t.Setenv("OCEL_VAR_KEY", "baked")
	t.Setenv("KEY", "bare")

	got := ocel.Env[struct {
		Key string `ocel:"KEY"`
	}]()
	if got.Key != "baked" {
		t.Errorf("Key = %q, want %q", got.Key, "baked")
	}
}

func TestEnvFallsBackToTheDefaultAndNowhereElse(t *testing.T) {
	t.Setenv("KEY", "set")
	got := ocel.Env[struct {
		Key   string `ocel:"KEY,default=d"`
		Unset string `ocel:"UNSET,default=d"`
	}]()
	if got.Key != "set" || got.Unset != "d" {
		t.Errorf("Env() = %+v", got)
	}
}

func TestEnvNamesTheFixingCommandWhenNothingIsSet(t *testing.T) {
	err := valueError(t, func() {
		ocel.Env[struct {
			Key string `ocel:"NOTHING_SET"`
		}]()
	})
	want := "'NOTHING_SET' has no value. Set one with `ocel env set NOTHING_SET <VALUE>`."
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err, want)
	}
}

func TestEnvFailsWhenTheValueIsPresentButOutOfType(t *testing.T) {
	t.Setenv("PORT", "eighty")
	err := valueError(t, func() {
		ocel.Env[struct {
			Port int `ocel:"PORT"`
		}]()
	})
	if !strings.Contains(err.Error(), "'PORT' is set but does not satisfy its type") || !strings.Contains(err.Error(), "eighty") || !strings.Contains(err.Error(), "`ocel env set PORT <VALUE>`") {
		t.Errorf("error = %q", err)
	}
}

func TestEnvKeepsAnEncryptedValueOutOfTheErrorARead(t *testing.T) {
	t.Setenv("LIMIT", "s3cret")
	err := valueError(t, func() {
		ocel.Env[struct {
			Limit int `ocel:"LIMIT,sensitive"`
		}]()
	})
	if strings.Contains(err.Error(), "s3cret") || !strings.Contains(err.Error(), "withheld") {
		t.Errorf("error = %q", err)
	}
}

func TestASecretResolvesOnEveryRead(t *testing.T) {
	t.Setenv("OCEL_VAR_SIGNING_KEY", "first")
	got := ocel.Env[struct {
		Key ocel.Secret `ocel:"SIGNING_KEY"`
	}]()

	if got.Key.Key() != "SIGNING_KEY" || got.Key.Value() != "first" {
		t.Fatalf("Key = %s, Value() = %q", got.Key, got.Key.Value())
	}
	t.Setenv("OCEL_VAR_SIGNING_KEY", "rotated")
	if v := got.Key.Value(); v != "rotated" {
		t.Errorf("Value() after rotation = %q, want %q", v, "rotated")
	}
}

func TestASecretWhoseValueVanishedFailsTheReadRatherThanReadEmpty(t *testing.T) {
	t.Setenv("SIGNING_KEY", "first")
	got := ocel.Env[struct {
		Key ocel.Secret `ocel:"SIGNING_KEY"`
	}]()

	t.Setenv("SIGNING_KEY", "")
	if v := got.Key.Value(); v != "" {
		t.Errorf("Value() of a set-but-empty variable = %q, want the empty value it holds", v)
	}
	if err := os.Unsetenv("SIGNING_KEY"); err != nil {
		t.Fatal(err)
	}
	err := valueError(t, func() { got.Key.Value() })
	if err.Key != "SIGNING_KEY" || !strings.Contains(err.Error(), "has no value") {
		t.Errorf("error = %q", err)
	}
}

func TestASecretPrintsARedactionUnderEveryVerb(t *testing.T) {
	t.Setenv("SIGNING_KEY", "s3cret")
	got := ocel.Env[struct {
		Key ocel.Secret `ocel:"SIGNING_KEY"`
	}]()

	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q"} {
		printed := fmt.Sprintf(verb, got.Key)
		if strings.Contains(printed, "s3cret") || !strings.Contains(printed, "SIGNING_KEY") {
			t.Errorf("%s = %q", verb, printed)
		}
		printed = fmt.Sprintf(verb, got)
		if strings.Contains(printed, "s3cret") {
			t.Errorf("%s of the struct = %q", verb, printed)
		}
	}
}

func TestASecretWithNoValueFailsAtInit(t *testing.T) {
	err := valueError(t, func() {
		ocel.Env[struct {
			Key ocel.Secret `ocel:"NOTHING_SET"`
		}]()
	})
	if err.Key != "NOTHING_SET" {
		t.Errorf("error = %q", err)
	}
}

func TestEnvRefusesAVariableThisAppsBindingPutsOutOfScope(t *testing.T) {
	t.Setenv("OCEL_APP_FOLDER", "/apps/api")
	t.Setenv("KEY", "v")
	err := caught(t, func() {
		ocel.Env[struct {
			Key string `ocel:"KEY,folders=/apps/web"`
		}]()
	})
	var scope *ocel.EnvScopeError
	if !errors.As(err, &scope) {
		t.Fatalf("error = %v, want an *EnvScopeError", err)
	}
	want := "'KEY' is scoped to /apps/web, but this app is bound to /apps/api. Bind this app to one of those folders in ocel.config.ts, or widen the variable's scope."
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err, want)
	}
}

func TestEnvReadsAVariableScopedToTheFolderThisAppIsBoundTo(t *testing.T) {
	t.Setenv("OCEL_APP_FOLDER", "/apps/web")
	t.Setenv("KEY", "v")
	got := ocel.Env[struct {
		Key string `ocel:"KEY,folders=/apps/api;/apps/web"`
	}]()
	if got.Key != "v" {
		t.Errorf("Key = %q, want %q", got.Key, "v")
	}
}

func TestDeploymentURLReadsWhatOcelWrote(t *testing.T) {
	t.Setenv("OCEL_URL", "https://web-j-1.ocel.site")
	got, err := ocel.DeploymentURL()
	if err != nil || got != "https://web-j-1.ocel.site" {
		t.Errorf("DeploymentURL() = %q, %v", got, err)
	}
}

func TestDeploymentURLFailsWhenNoneWasDelivered(t *testing.T) {
	_, err := ocel.DeploymentURL()
	var value *ocel.EnvValueError
	if !errors.As(err, &value) || value.Key != "OCEL_URL" {
		t.Errorf("DeploymentURL() error = %v, want an *EnvValueError for OCEL_URL", err)
	}
}
