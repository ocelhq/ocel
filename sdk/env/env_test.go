package env_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ocel.dev/env"
	"ocel.dev/internal/sdktest"
)

type cell struct {
	Key    string `json:"key"`
	Folder string `json:"folder"`
	Value  string `json:"value"`
}

func variablesServer(t *testing.T, cells []cell, seen *[]map[string]any) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+sdktest.DevServerToken {
			http.Error(w, "this request has no valid session token", http.StatusForbidden)
			return
		}
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
	t.Setenv("OCEL_DEV_SERVER_TOKEN", sdktest.DevServerToken)
	return &seen
}

func definitionError(t *testing.T, run func()) *env.DefinitionError {
	t.Helper()
	err := sdktest.PanicError(t, run)
	var definition *env.DefinitionError
	if !errors.As(err, &definition) {
		t.Fatalf("error = %v, want an *DefinitionError", err)
	}
	return definition
}

func valueError(t *testing.T, run func()) *env.ValueError {
	t.Helper()
	err := sdktest.PanicError(t, run)
	var value *env.ValueError
	if !errors.As(err, &value) {
		t.Fatalf("error = %v, want an *ValueError", err)
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

func TestDefineRejectsAnUnusableKey(t *testing.T) {
	type vars struct {
		Name string `ocel:"lower-case"`
	}
	err := definitionError(t, func() { env.MustDefine[vars]() })
	if !strings.Contains(err.Error(), "'lower-case' is not a usable variable name") {
		t.Errorf("error = %q", err)
	}
}

func TestDefineRejectsAnUnusableDescription(t *testing.T) {
	t.Run("control", func(t *testing.T) {
		type vars struct {
			Name string "ocel:\"NAME,description=two\\nlines\""
		}
		err := definitionError(t, func() { env.MustDefine[vars]() })
		if !strings.Contains(err.Error(), "unusable description") {
			t.Errorf("error = %q", err)
		}
	})
	t.Run("too long", func(t *testing.T) {
		type vars struct {
			Name string "ocel:\"NAME,description=xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx\""
		}
		err := definitionError(t, func() { env.MustDefine[vars]() })
		if !strings.Contains(err.Error(), "unusable description") {
			t.Errorf("error = %q", err)
		}
	})
}

func TestDefineRejectsAnOcelOwnedNameUnderEveryClass(t *testing.T) {
	for class, run := range map[string]func(){
		"plain": func() {
			env.MustDefine[struct {
				Name string `ocel:"OCEL_THING"`
			}]()
		},
		"sensitive": func() {
			env.MustDefine[struct {
				Name string `ocel:"OCEL_THING,sensitive"`
			}]()
		},
		"secret": func() {
			env.MustDefine[struct {
				Name env.Secret `ocel:"OCEL_THING"`
			}]()
		},
	} {
		err := definitionError(t, run)
		if !strings.Contains(err.Error(), "reserved prefix OCEL_") {
			t.Errorf("%s: error = %q", class, err)
		}
	}
}

func TestDefineRejectsTheDeploymentURLUnderEveryClass(t *testing.T) {
	for _, class := range []string{"plain", "sensitive", "secret"} {
		t.Run(class, func(t *testing.T) {
			err := sdktest.PanicError(t, func() {
				switch class {
				case "plain":
					env.MustDefine[struct {
						URL string `ocel:"OCEL_URL"`
					}]()
				case "sensitive":
					env.MustDefine[struct {
						URL string `ocel:"OCEL_URL,sensitive"`
					}]()
				case "secret":
					env.MustDefine[struct {
						URL env.Secret `ocel:"OCEL_URL"`
					}]()
				}
			})
			if err == nil || !strings.Contains(err.Error(), "Read it with DeploymentURL") {
				t.Errorf("error = %v", err)
			}
		})
	}
}

func TestDefineRejectsADefaultOnASecret(t *testing.T) {
	type vars struct {
		Key env.Secret `ocel:"SIGNING_KEY,default=x"`
	}
	err := definitionError(t, func() { env.MustDefine[vars]() })
	if !strings.Contains(err.Error(), "A live value must fail loudly") {
		t.Errorf("error = %q", err)
	}
}

func TestDefineRejectsAnOptionalSecret(t *testing.T) {
	type vars struct {
		Key *env.Secret `ocel:"SIGNING_KEY"`
	}
	err := definitionError(t, func() { env.MustDefine[vars]() })
	if !strings.Contains(err.Error(), "A live value must fail loudly") {
		t.Errorf("error = %q", err)
	}
}

func TestDefineRejectsTheSecretClassAsATagOption(t *testing.T) {
	type vars struct {
		Key string `ocel:"SIGNING_KEY,secret"`
	}
	err := definitionError(t, func() { env.MustDefine[vars]() })
	if !strings.Contains(err.Error(), "make it an env.Secret and drop the option") {
		t.Errorf("error = %q", err)
	}
}

func TestDefineRejectsAClassOptionOnASecret(t *testing.T) {
	type vars struct {
		Key env.Secret `ocel:"SIGNING_KEY,sensitive"`
	}
	err := definitionError(t, func() { env.MustDefine[vars]() })
	if !strings.Contains(err.Error(), "A Secret field is always the secret class") {
		t.Errorf("error = %q", err)
	}
}

func TestDefineDeclaresTheClassTheTagNames(t *testing.T) {
	seen := discover(t, []cell{{Key: "P", Value: "v"}, {Key: "S", Value: "v"}, {Key: "E", Value: "v"}, {Key: "X"}})

	env.MustDefine[struct {
		Plain     string     `ocel:"P"`
		Sensitive string     `ocel:"S,sensitive,description=Used to call Stripe"`
		Explicit  string     `ocel:"E,plain"`
		Secret    env.Secret `ocel:"X"`
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
	if got := declared["definitions"].([]any)[1].(map[string]any)["description"]; got != "Used to call Stripe" {
		t.Errorf("description = %q, want %q", got, "Used to call Stripe")
	}
}

func TestDefineRejectsAnUnknownTagOption(t *testing.T) {
	type vars struct {
		Key string `ocel:"KEY,required"`
	}
	err := definitionError(t, func() { env.MustDefine[vars]() })
	if !strings.Contains(err.Error(), "unknown tag option 'required'") {
		t.Errorf("error = %q", err)
	}
}

func TestDefineRejectsAnUnusableFolderScope(t *testing.T) {
	for _, tc := range []struct{ folders, want string }{
		{"apps/web", "must start with '/'"},
		{"/", "'/' is the project root"},
		{"/apps/web/", "must not end with '/'"},
		{"/apps//web", "no empty segments"},
		{"/apps/web;/apps/web", "is named twice"},
	} {
		err := sdktest.PanicError(t, func() {
			switch tc.folders {
			case "apps/web":
				env.MustDefine[struct {
					K string `ocel:"KEY,folders=apps/web"`
				}]()
			case "/":
				env.MustDefine[struct {
					K string `ocel:"KEY,folders=/"`
				}]()
			case "/apps/web/":
				env.MustDefine[struct {
					K string `ocel:"KEY,folders=/apps/web/"`
				}]()
			case "/apps//web":
				env.MustDefine[struct {
					K string `ocel:"KEY,folders=/apps//web"`
				}]()
			case "/apps/web;/apps/web":
				env.MustDefine[struct {
					K string `ocel:"KEY,folders=/apps/web;/apps/web"`
				}]()
			}
		})
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("folders=%s: error = %v, want %q", tc.folders, err, tc.want)
		}
	}
}

func TestDefineRejectsAFieldNothingParsesInto(t *testing.T) {
	type vars struct {
		Key []string `ocel:"KEY"`
	}
	err := definitionError(t, func() { env.MustDefine[vars]() })
	if !strings.Contains(err.Error(), "is read into a []string, which nothing parses a value into") {
		t.Errorf("error = %q", err)
	}
}

func TestDefineRejectsADefaultItsOwnTypeRejects(t *testing.T) {
	type vars struct {
		Port int `ocel:"PORT,default=eighty"`
	}
	err := definitionError(t, func() { env.MustDefine[vars]() })
	if !strings.Contains(err.Error(), "has a default its own type rejects") {
		t.Errorf("error = %q", err)
	}
}

func TestDefineRejectsAnUntaggedField(t *testing.T) {
	type vars struct {
		Name string
	}
	err := definitionError(t, func() { env.MustDefine[vars]() })
	if !strings.Contains(err.Error(), "field Name has no `ocel` tag") {
		t.Errorf("error = %q", err)
	}
}

func TestDefineRejectsAKeyDeclaredTwiceInOneStruct(t *testing.T) {
	type vars struct {
		A string `ocel:"TWICE"`
		B string `ocel:"TWICE"`
	}
	err := definitionError(t, func() { env.MustDefine[vars]() })
	if !strings.Contains(err.Error(), "'TWICE' is declared by two fields") {
		t.Errorf("error = %q", err)
	}
}

func TestDefineRejectsANonStruct(t *testing.T) {
	err := definitionError(t, func() { env.MustDefine[string]() })
	if !strings.Contains(err.Error(), "env.Define wants a struct type") {
		t.Errorf("error = %q", err)
	}
}

func TestDefineLetsTheFileThatDeclaredAKeyDeclareItAgain(t *testing.T) {
	t.Setenv("OWNED", "v")
	type vars struct {
		Owned string `ocel:"OWNED"`
	}
	env.MustDefine[vars]()
	if err := sdktest.PanicError(t, func() { env.MustDefine[vars]() }); err != nil {
		t.Errorf("second declaration from the same file failed: %v", err)
	}
}

func TestDefineDeclaresEveryVariableWithItsClassAndWhetherItIsRequired(t *testing.T) {
	seen := discover(t, nil)

	type vars struct {
		Plain     string        `ocel:"PLAIN"`
		Sensitive string        `ocel:"SENSITIVE,sensitive"`
		Secret    env.Secret    `ocel:"SECRET"`
		Defaulted int           `ocel:"DEFAULTED,default=3"`
		Optional  *bool         `ocel:"OPTIONAL"`
		Scoped    time.Duration `ocel:"SCOPED,folders=/apps/web;/apps/api"`
	}
	got := env.MustDefine[vars]()

	if got != (vars{}) {
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
		{"key": "DEFAULTED", "class": "VARIABLE_CLASS_PLAIN"},
		{"key": "OPTIONAL", "class": "VARIABLE_CLASS_PLAIN"},
		{"key": "SCOPED", "class": "VARIABLE_CLASS_PLAIN", "required": true, "folders": []any{"/apps/web", "/apps/api"}},
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
	if byPath(*seen, "/ReportEnvProblems") != nil {
		t.Error("a ReportEnvProblems call, want none: the CLI works out which required cells are missing")
	}
}

func equal(got, want any) bool {
	g, _ := json.Marshal(got)
	w, _ := json.Marshal(want)
	return string(g) == string(w)
}

func TestDefineLeavesARequiredKeyTheStoreHasNoCellForToTheCLI(t *testing.T) {
	seen := discover(t, []cell{{Key: "PRESENT", Value: "v"}})

	env.MustDefine[struct {
		Present string `ocel:"PRESENT"`
		Missing string `ocel:"MISSING"`
	}]()

	if problems := reported(t, *seen); len(problems) != 0 {
		t.Fatalf("problems = %v, want none: the CLI works out which required cells are missing", problems)
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

func TestDefineReportsAPresentValueItsTypeRejects(t *testing.T) {
	seen := discover(t, []cell{{Key: "PORT", Value: "eighty"}, {Key: "PORT", Folder: "/apps/web", Value: "80"}})

	env.MustDefine[struct {
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

func TestDefineKeepsAnEncryptedValueOutOfTheProblemItReports(t *testing.T) {
	seen := discover(t, []cell{{Key: "LIMIT", Value: "s3cret"}})

	env.MustDefine[struct {
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

func TestDefineSaysNothingWhenEveryCellSatisfiesItsType(t *testing.T) {
	seen := discover(t, []cell{{Key: "PORT", Value: "80"}, {Key: "NAME", Value: "n"}})

	env.MustDefine[struct {
		Port int    `ocel:"PORT"`
		Name string `ocel:"NAME"`
		Opt  *int   `ocel:"OPT"`
		Def  int    `ocel:"DEF,default=1"`
	}]()

	if problems := reported(t, *seen); problems != nil {
		t.Errorf("problems = %v, want none", problems)
	}
}

func TestDefineTakesALiveCellAsPresentWithoutAValueAndNeverChecksIt(t *testing.T) {
	seen := discover(t, []cell{{Key: "KEY"}})

	env.MustDefine[struct {
		Key env.Secret `ocel:"KEY"`
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

func TestDefineParsesEveryFieldIntoItsType(t *testing.T) {
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

	type vars struct {
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
	got := env.MustDefine[vars]()

	if got.Str != "s" || !got.B || got.I != -7 || got.U8 != 255 || got.UP != 4096 || got.F != 1.5 || got.D != 90*time.Second || got.Level != 2 || got.Addr != netip.MustParseAddr("10.0.0.1") || got.Opt == nil || *got.Opt != 9 || got.Unset != nil || got.Def != 42 {
		t.Errorf("Env() = %+v", got)
	}
}

func TestDefineReadsASensitiveValueUnderTheNameTheProcessEnvironmentHolds(t *testing.T) {
	t.Setenv("KEY", "baked")

	got := env.MustDefine[struct {
		Key string `ocel:"KEY,sensitive"`
	}]()
	if got.Key != "baked" || os.Getenv("KEY") != "baked" {
		t.Errorf("Key = %q, os.Getenv = %q, want both %q", got.Key, os.Getenv("KEY"), "baked")
	}
}

func TestDefineIgnoresAValueUnderTheNameTheRuntimeOnceNamespacedASealedValueWith(t *testing.T) {
	t.Setenv("OCEL_VAR_UNPREFIXED", "baked")

	err := valueError(t, func() {
		env.MustDefine[struct {
			Key string `ocel:"UNPREFIXED,sensitive"`
		}]()
	})
	if err.Key != "UNPREFIXED" {
		t.Errorf("error = %v, want UNPREFIXED unset", err)
	}
}

func TestDefineFallsBackToTheDefaultAndNowhereElse(t *testing.T) {
	t.Setenv("KEY", "set")
	got := env.MustDefine[struct {
		Key   string `ocel:"KEY,default=d"`
		Unset string `ocel:"UNSET,default=d"`
	}]()
	if got.Key != "set" || got.Unset != "d" {
		t.Errorf("Env() = %+v", got)
	}
}

func TestDefineNamesTheFixingCommandWhenNothingIsSet(t *testing.T) {
	err := valueError(t, func() {
		env.MustDefine[struct {
			Key string `ocel:"NOTHING_SET"`
		}]()
	})
	want := "'NOTHING_SET' has no value. Set one with `ocel env set NOTHING_SET=<VALUE>`."
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err, want)
	}
}

func TestDefineFailsWhenTheValueIsPresentButOutOfType(t *testing.T) {
	t.Setenv("PORT", "eighty")
	err := valueError(t, func() {
		env.MustDefine[struct {
			Port int `ocel:"PORT"`
		}]()
	})
	if !strings.Contains(err.Error(), "'PORT' is set but does not satisfy its type") || !strings.Contains(err.Error(), "eighty") || !strings.Contains(err.Error(), "`ocel env set PORT=<VALUE>`") {
		t.Errorf("error = %q", err)
	}
}

func TestDefineKeepsAnEncryptedValueOutOfTheErrorARead(t *testing.T) {
	t.Setenv("LIMIT", "s3cret")
	err := valueError(t, func() {
		env.MustDefine[struct {
			Limit int `ocel:"LIMIT,sensitive"`
		}]()
	})
	if strings.Contains(err.Error(), "s3cret") || !strings.Contains(err.Error(), "withheld") {
		t.Errorf("error = %q", err)
	}
}

func TestASecretResolvesOnEveryRead(t *testing.T) {
	t.Setenv("SIGNING_KEY", "first")
	got := env.MustDefine[struct {
		Key env.Secret `ocel:"SIGNING_KEY"`
	}]()

	if got.Key.Key() != "SIGNING_KEY" || got.Key.Value() != "first" {
		t.Fatalf("Key = %s, Value() = %q", got.Key, got.Key.Value())
	}
	t.Setenv("SIGNING_KEY", "rotated")
	if v := got.Key.Value(); v != "rotated" {
		t.Errorf("Value() after rotation = %q, want %q", v, "rotated")
	}
}

func TestDefineReadsAValueFromTheLiveDirectoryWhenNoVariableSetsIt(t *testing.T) {
	t.Setenv("OCEL_LIVE_DIR", liveDir(t, map[string]string{"FILE_ONLY": "from the file\n"}))

	got := env.MustDefine[struct {
		Key string `ocel:"FILE_ONLY"`
	}]()
	if got.Key != "from the file\n" {
		t.Errorf("Key = %q, want the file's bytes verbatim", got.Key)
	}
}

func TestDefinePrefersADeliveredVariableOverTheLiveDirectoryFileOfTheSameKey(t *testing.T) {
	dir := liveDir(t, map[string]string{"FILE_SHADOWED": "from the file", "FILE_BARE": "from the file"})
	t.Setenv("OCEL_LIVE_DIR", dir)
	t.Setenv("FILE_SHADOWED", "baked")
	t.Setenv("FILE_BARE", "bare")

	got := env.MustDefine[struct {
		Shadowed string `ocel:"FILE_SHADOWED"`
		Bare     string `ocel:"FILE_BARE"`
	}]()
	if got.Shadowed != "baked" || got.Bare != "bare" {
		t.Errorf("Env() = %+v", got)
	}
}

func TestDefineTakesAKeyTheLiveDirectoryHasNoFileForAsUnset(t *testing.T) {
	t.Setenv("OCEL_LIVE_DIR", liveDir(t, nil))

	err := valueError(t, func() {
		env.MustDefine[struct {
			Key string `ocel:"FILE_MISSING"`
		}]()
	})
	if err.Key != "FILE_MISSING" || !strings.Contains(err.Error(), "has no value") {
		t.Errorf("error = %q", err)
	}
}

func TestASecretReadsTheRotatedLiveDirectoryFileOnTheNextCall(t *testing.T) {
	dir := liveDir(t, map[string]string{"ROTATING_KEY": "first"})
	t.Setenv("OCEL_LIVE_DIR", dir)

	got := env.MustDefine[struct {
		Key env.Secret `ocel:"ROTATING_KEY"`
	}]()
	if v := got.Key.Value(); v != "first" {
		t.Fatalf("Value() = %q, want %q", v, "first")
	}

	write(t, filepath.Join(dir, "ROTATING_KEY"), "rotated")
	if v := got.Key.Value(); v != "rotated" {
		t.Errorf("Value() after rotation = %q, want %q", v, "rotated")
	}
}

func liveDir(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for key, value := range files {
		write(t, filepath.Join(dir, key), value)
	}
	return dir
}

func write(t *testing.T, path, value string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestASecretWhoseValueVanishedFailsTheReadRatherThanReadEmpty(t *testing.T) {
	t.Setenv("SIGNING_KEY", "first")
	got := env.MustDefine[struct {
		Key env.Secret `ocel:"SIGNING_KEY"`
	}]()

	t.Setenv("SIGNING_KEY", "")
	if v := got.Key.Value(); v != "" {
		t.Errorf("Value() of a set-but-empty variable = %q, want the empty value it is set to", v)
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
	got := env.MustDefine[struct {
		Key env.Secret `ocel:"SIGNING_KEY"`
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
		env.MustDefine[struct {
			Key env.Secret `ocel:"NOTHING_SET"`
		}]()
	})
	if err.Key != "NOTHING_SET" {
		t.Errorf("error = %q", err)
	}
}

func TestDefineRefusesAVariableThisAppsBindingPutsOutOfScope(t *testing.T) {
	t.Setenv("OCEL_APP_FOLDER", "/apps/api")
	t.Setenv("KEY", "v")
	err := sdktest.PanicError(t, func() {
		env.MustDefine[struct {
			Key string `ocel:"KEY,folders=/apps/web"`
		}]()
	})
	var scope *env.ScopeError
	if !errors.As(err, &scope) {
		t.Fatalf("error = %v, want an *ScopeError", err)
	}
	want := "'KEY' is scoped to /apps/web, but this app is bound to /apps/api. Bind this app to one of those folders in ocel.json, or widen the variable's scope."
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err, want)
	}
}

func TestDefineReadsAVariableScopedToTheFolderThisAppIsBoundTo(t *testing.T) {
	t.Setenv("OCEL_APP_FOLDER", "/apps/web")
	t.Setenv("KEY", "v")
	got := env.MustDefine[struct {
		Key string `ocel:"KEY,folders=/apps/api;/apps/web"`
	}]()
	if got.Key != "v" {
		t.Errorf("Key = %q, want %q", got.Key, "v")
	}
}

func TestDeploymentURLReadsWhatOcelWrote(t *testing.T) {
	t.Setenv("OCEL_URL", "https://web-j-1.ocel.site")
	got, err := env.DeploymentURL()
	if err != nil || got != "https://web-j-1.ocel.site" {
		t.Errorf("DeploymentURL() = %q, %v", got, err)
	}
}

func TestDeploymentURLFailsWhenNoneWasDelivered(t *testing.T) {
	_, err := env.DeploymentURL()
	var value *env.ValueError
	if !errors.As(err, &value) || value.Key != "OCEL_URL" {
		t.Errorf("DeploymentURL() error = %v, want an *ValueError for %s", err, "OCEL_URL")
	}
}

type githubGroup struct {
	ID     string `ocel:"GITHUB_ID"`
	Secret string `ocel:"GITHUB_SECRET"`
}

func TestDefineLeavesAnOptionalGroupAbsentWhenNoMemberIsDelivered(t *testing.T) {
	type vars struct {
		Region string       `ocel:"REGION,default=eu"`
		GitHub *githubGroup `ocel:"github"`
	}
	got := env.MustDefine[vars]()
	if got.GitHub != nil {
		t.Errorf("GitHub = %#v, want nil", got.GitHub)
	}
	if got.Region != "eu" {
		t.Errorf("Region = %q, want %q", got.Region, "eu")
	}
}

func TestDefineResolvesAFullyDeliveredOptionalGroup(t *testing.T) {
	t.Setenv("GITHUB_ID", "id")
	t.Setenv("GITHUB_SECRET", "s3cret")

	got := env.MustDefine[struct {
		GitHub *githubGroup `ocel:"github"`
	}]()

	if got.GitHub == nil {
		t.Fatal("GitHub = nil, want the delivered group")
	}
	if got.GitHub.ID != "id" || got.GitHub.Secret != "s3cret" {
		t.Errorf("GitHub = %+v", *got.GitHub)
	}
}

type mixedGroup struct {
	Anchor   string  `ocel:"MIXED_ANCHOR"`
	Optional *string `ocel:"MIXED_OPTIONAL"`
	Fallback string  `ocel:"MIXED_FALLBACK,default=d"`
}

func TestDefineDoesNotRequireAGroupMemberSpelledOptionalWhileTheGroupIsOn(t *testing.T) {
	seen := discover(t, []cell{{Key: "MIXED_ANCHOR", Value: "a"}})

	env.MustDefine[struct {
		Mixed *mixedGroup `ocel:"mixed"`
	}]()

	if problems := reported(t, *seen); problems != nil {
		t.Errorf("problems = %v, want none", problems)
	}
	declared := byPath(*seen, "/DeclareEnv")
	for _, raw := range declared["definitions"].([]any) {
		definition := raw.(map[string]any)
		required := definition["required"] == true
		if want := definition["key"] == "MIXED_ANCHOR"; required != want {
			t.Errorf("%v required = %v, want %v", definition["key"], required, want)
		}
	}
}

func TestDefineResolvesAGroupMemberSpelledOptionalToNothing(t *testing.T) {
	t.Setenv("MIXED_ANCHOR", "a")

	got := env.MustDefine[struct {
		Mixed *mixedGroup `ocel:"mixed"`
	}]()

	if got.Mixed == nil {
		t.Fatal("Mixed = nil, want the group its anchor turned on")
	}
	if got.Mixed.Optional != nil || got.Mixed.Fallback != "d" {
		t.Errorf("Mixed = %+v", *got.Mixed)
	}
}

func TestDefineResolvesASecretInsideAGroup(t *testing.T) {
	type stripe struct {
		Account string     `ocel:"STRIPE_ACCOUNT"`
		Key     env.Secret `ocel:"STRIPE_KEY"`
	}
	t.Setenv("STRIPE_ACCOUNT", "acct")
	t.Setenv("STRIPE_KEY", "first")

	got := env.MustDefine[struct {
		Stripe stripe `ocel:"stripe"`
	}]()

	if got.Stripe.Key.Key() != "STRIPE_KEY" || got.Stripe.Key.Value() != "first" {
		t.Fatalf("Key = %s, Value() = %q", got.Stripe.Key, got.Stripe.Key.Value())
	}
	t.Setenv("STRIPE_KEY", "rotated")
	if v := got.Stripe.Key.Value(); v != "rotated" {
		t.Errorf("Value() after rotation = %q, want %q", v, "rotated")
	}
}

func TestDefineRefusesAGroupNestedInAGroup(t *testing.T) {
	type inner struct {
		Key string `ocel:"INNER_KEY"`
	}
	type outer struct {
		Key   string `ocel:"OUTER_KEY"`
		Inner inner  `ocel:"inner"`
	}
	err := definitionError(t, func() {
		env.MustDefine[struct {
			Outer outer `ocel:"outer"`
		}]()
	})
	if !strings.Contains(err.Error(), "Groups nest one level only") {
		t.Errorf("error = %q", err)
	}
}

func TestDefineDeclaresGroupsInDeclarationOrderWithWhatTurningThemOnDoes(t *testing.T) {
	type mail struct {
		Host string `ocel:"MAIL_HOST"`
	}
	type auth struct {
		ID string `ocel:"AUTH_ID"`
	}
	seen := discover(t, []cell{{Key: "MAIL_HOST", Value: "h"}, {Key: "AUTH_ID", Value: "i"}})

	env.MustDefine[struct {
		Mail mail  `ocel:"mail,description=Send mail"`
		Auth *auth `ocel:"auth,description=Enable sign-in"`
	}]()

	declared := byPath(*seen, "/DeclareEnv")
	want := []any{
		map[string]any{"key": "mail", "required": true, "description": "Send mail"},
		map[string]any{"key": "auth", "description": "Enable sign-in"},
	}
	if got := declared["groups"]; !equal(got, want) {
		t.Errorf("groups = %v, want %v", got, want)
	}
	for _, raw := range declared["definitions"].([]any) {
		definition := raw.(map[string]any)
		wantGroup := "mail"
		if definition["key"] == "AUTH_ID" {
			wantGroup = "auth"
		}
		if definition["group"] != wantGroup {
			t.Errorf("%v group = %v, want %q", definition["key"], definition["group"], wantGroup)
		}
	}
}

func TestDefineRefusesAKeyDeclaredBothInAGroupAndAtTopLevel(t *testing.T) {
	type inGroup struct {
		Key string `ocel:"SHARED_KEY"`
	}
	err := definitionError(t, func() {
		env.MustDefine[struct {
			Group inGroup `ocel:"grouped"`
			Loose string  `ocel:"SHARED_KEY"`
		}]()
	})
	if !strings.Contains(err.Error(), "'SHARED_KEY' is declared by two fields") {
		t.Errorf("error = %q", err)
	}
}

func TestDefineRefusesTwoGroupsUnderOneName(t *testing.T) {
	type first struct {
		Key string `ocel:"FIRST_KEY"`
	}
	type second struct {
		Key string `ocel:"SECOND_KEY"`
	}
	err := definitionError(t, func() {
		env.MustDefine[struct {
			First  first  `ocel:"twice"`
			Second second `ocel:"twice"`
		}]()
	})
	if !strings.Contains(err.Error(), "twice") {
		t.Errorf("error = %q", err)
	}
}

func TestDefineRefusesAnUnexportedGroup(t *testing.T) {
	type hidden struct {
		Key string `ocel:"HIDDEN_KEY"`
	}
	err := definitionError(t, func() {
		env.MustDefine[struct {
			hidden hidden `ocel:"hidden"`
		}]()
	})
	if !strings.Contains(err.Error(), "unexported") {
		t.Errorf("error = %q", err)
	}
}

func TestDefineRefusesAGroupThatDeclaresNoVariables(t *testing.T) {
	type empty struct{}
	err := definitionError(t, func() {
		env.MustDefine[struct {
			Empty empty `ocel:"empty"`
		}]()
	})
	if !strings.Contains(err.Error(), "declares no variables") {
		t.Errorf("error = %q", err)
	}
}

func TestDefineRefusesAGroupNoFolderCanSatisfy(t *testing.T) {
	type split struct {
		Web string `ocel:"SPLIT_WEB,folders=/web"`
		API string `ocel:"SPLIT_API,folders=/api"`
	}
	err := definitionError(t, func() {
		env.MustDefine[struct {
			Split split `ocel:"split"`
		}]()
	})
	for _, want := range []string{"split", "SPLIT_WEB", "/web", "SPLIT_API", "/api", "read as one"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want %q in it", err, want)
		}
	}
}

func TestDefineAcceptsAGroupWhoseMembersShareAFolder(t *testing.T) {
	type shared struct {
		Both  string `ocel:"SHARED_BOTH,folders=/web;/api"`
		API   string `ocel:"SHARED_API,folders=/api"`
		Wider string `ocel:"SHARED_WIDER"`
	}
	t.Setenv("OCEL_APP_FOLDER", "/api")
	t.Setenv("SHARED_BOTH", "b")
	t.Setenv("SHARED_API", "a")
	t.Setenv("SHARED_WIDER", "w")
	if err := sdktest.PanicError(t, func() {
		env.MustDefine[struct {
			Shared shared `ocel:"shared"`
		}]()
	}); err != nil {
		t.Errorf("error = %v, want a group /api satisfies accepted", err)
	}
}
