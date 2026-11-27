package resourcesv1_test

import (
	"errors"
	"strings"
	"testing"

	"buf.build/go/protovalidate"
	"google.golang.org/protobuf/proto"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
)

func TestProtovalidateAcceptsEveryVariableDefinitionAnSDKCanDeclare(t *testing.T) {
	cases := map[string]*resourcesv1.VariableDefinition{
		"a required secret": {
			Key:      "API_KEY",
			Class:    resourcesv1.VariableClass_VARIABLE_CLASS_SECRET,
			Required: true,
		},
		"a plain default with its text": {
			Key:          "GREETING",
			Class:        resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN,
			Defaulted:    true,
			DefaultValue: proto.String("hello"),
		},
		"a plain default that is the empty string": {
			Key:          "SUFFIX",
			Class:        resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN,
			Defaulted:    true,
			DefaultValue: proto.String(""),
		},
		"a plain default whose text the SDK cannot write": {
			Key:       "ORIGINS",
			Class:     resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN,
			Defaulted: true,
		},
		"a sensitive default, flagged only": {
			Key:       "WEBHOOK_KEY",
			Class:     resourcesv1.VariableClass_VARIABLE_CLASS_SENSITIVE,
			Defaulted: true,
		},
		"an optional variable": {
			Key:   "SENTRY_DSN",
			Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN,
		},
		"a typed integer": {
			Key:       "PORT",
			Class:     resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN,
			ValueType: resourcesv1.ValueType_VALUE_TYPE_INTEGER,
			Required:  true,
		},
		"a sensitive boolean": {
			Key:       "BETA",
			Class:     resourcesv1.VariableClass_VARIABLE_CLASS_SENSITIVE,
			ValueType: resourcesv1.ValueType_VALUE_TYPE_BOOLEAN,
			Required:  true,
		},
		"text with choices": {
			Key:      "LOG_LEVEL",
			Class:    resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN,
			Choices:  []string{"debug", "info", "warn"},
			Required: true,
		},
	}
	for name, definition := range cases {
		t.Run(name, func(t *testing.T) {
			if err := protovalidate.Validate(definition); err != nil {
				t.Fatalf("expected protovalidate to accept it, got %v", err)
			}
		})
	}
}

func TestProtovalidateRefusesAVariableDefinitionThatBreaksARule(t *testing.T) {
	cases := map[string]struct {
		definition *resourcesv1.VariableDefinition
		rule       string
	}{
		"a required variable with a default": {
			definition: &resourcesv1.VariableDefinition{Key: "PORT", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Required: true, Defaulted: true},
			rule:       "variables.definition.defaulted_not_required",
		},
		"a secret with a default": {
			definition: &resourcesv1.VariableDefinition{Key: "API_KEY", Class: resourcesv1.VariableClass_VARIABLE_CLASS_SECRET, Defaulted: true},
			rule:       "variables.definition.secret_no_default",
		},
		"a sensitive default's text": {
			definition: &resourcesv1.VariableDefinition{Key: "WEBHOOK_KEY", Class: resourcesv1.VariableClass_VARIABLE_CLASS_SENSITIVE, Defaulted: true, DefaultValue: proto.String("k")},
			rule:       "variables.definition.default_value_plain",
		},
		"a default's text without the flag": {
			definition: &resourcesv1.VariableDefinition{Key: "GREETING", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, DefaultValue: proto.String("hello")},
			rule:       "variables.definition.default_value_plain",
		},
		"a typed secret": {
			definition: &resourcesv1.VariableDefinition{Key: "API_KEY", Class: resourcesv1.VariableClass_VARIABLE_CLASS_SECRET, Required: true, ValueType: resourcesv1.ValueType_VALUE_TYPE_INTEGER},
			rule:       "variables.definition.secret_untyped",
		},
		"a secret with choices": {
			definition: &resourcesv1.VariableDefinition{Key: "API_KEY", Class: resourcesv1.VariableClass_VARIABLE_CLASS_SECRET, Required: true, Choices: []string{"a"}},
			rule:       "variables.definition.secret_untyped",
		},
		"choices on a typed variable": {
			definition: &resourcesv1.VariableDefinition{Key: "PORT", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, ValueType: resourcesv1.ValueType_VALUE_TYPE_INTEGER, Choices: []string{"80"}},
			rule:       "variables.definition.choices_text",
		},
		"a default's text over 1024 bytes": {
			definition: &resourcesv1.VariableDefinition{Key: "BANNER", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Defaulted: true, DefaultValue: proto.String(strings.Repeat("a", 1025))},
			rule:       "string.max_bytes",
		},
		"a choice named twice": {
			definition: &resourcesv1.VariableDefinition{Key: "LOG_LEVEL", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Choices: []string{"info", "info"}},
			rule:       "repeated.unique",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			err := protovalidate.Validate(tc.definition)
			var violations *protovalidate.ValidationError
			if !errors.As(err, &violations) {
				t.Fatalf("expected a validation error, got %v", err)
			}
			for _, violation := range violations.Violations {
				if violation.Proto.GetRuleId() == tc.rule {
					return
				}
			}
			t.Fatalf("expected rule %s to refuse it, got %v", tc.rule, err)
		})
	}
}
