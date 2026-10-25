package project

import (
	"slices"
	"strings"
	"testing"
)

func TestAVariableIsBoundToTheLastDottedPropertyOfThePublishedRecordItNames(t *testing.T) {
	cfg := mustLoadJSON(t, `{"slug":"shop","bindings":{"variables":{
		"CLOUD_ORIGIN_CERT":"@origin.certificate",
		"CLOUD_SEAL_KEY":"@cloud.keys.seal"
	}}}`)

	want := []BoundVariable{
		{Key: "CLOUD_ORIGIN_CERT", Binding: "origin", Property: "certificate"},
		{Key: "CLOUD_SEAL_KEY", Binding: "cloud.keys", Property: "seal"},
	}
	if !slices.Equal(cfg.BoundVariables, want) {
		t.Errorf("BoundVariables = %+v, want %+v", cfg.BoundVariables, want)
	}
	if len(cfg.Bindings) != 0 {
		t.Errorf("Bindings = %+v, want no resource bound by a variable", cfg.Bindings)
	}
}

func TestABoundVariableIsRefusedWhereItNamesNoRecordOrProperty(t *testing.T) {
	for spelled, want := range map[string]string{
		`"origin.certificate"`:        `"@origin.certificate"`,
		`"@origin"`:                   "names no property",
		`"@origin."`:                  "names no property",
		`"@.certificate"`:             "names no published record",
		`"@ocel:postgres.orders.key"`: "ocel:",
	} {
		_, err := loadJSON(t, `{"slug":"shop","bindings":{"variables":{"CLOUD_ORIGIN_CERT":`+spelled+`}}}`)
		if err == nil || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "CLOUD_ORIGIN_CERT") {
			t.Errorf("bindings.variables.CLOUD_ORIGIN_CERT = %s: err = %v, want it refused naming the variable and %q", spelled, err, want)
		}
	}
}

func TestABoundVariableIsRefusedUnderANameNoAppCanDeclare(t *testing.T) {
	for key, want := range map[string]string{
		"cloud_origin": "upper-case",
		"OCEL_ORIGIN":  "OCEL_",
	} {
		_, err := loadJSON(t, `{"slug":"shop","bindings":{"variables":{"`+key+`":"@origin.certificate"}}}`)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("bindings.variables.%s: err = %v, want it refused mentioning %q", key, err, want)
		}
	}
}
