package proto_test

import (
	"io/fs"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	validate "buf.build/gen/go/bufbuild/protovalidate/protocolbuffers/go/buf/validate"
	"buf.build/go/protovalidate"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	bucketv1 "github.com/ocelhq/ocel/pkg/proto/app/bucket/v1"
	_ "github.com/ocelhq/ocel/pkg/proto/app/realtime/v1"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	_ "github.com/ocelhq/ocel/pkg/proto/app/task/v1"
	_ "github.com/ocelhq/ocel/pkg/proto/app/topic/v1"
	_ "github.com/ocelhq/ocel/pkg/proto/cli/help/v1"
	_ "github.com/ocelhq/ocel/pkg/proto/cli/result/v1"
	_ "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	_ "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	_ "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	_ "github.com/ocelhq/ocel/pkg/proto/common/plan/v1"
	_ "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	_ "github.com/ocelhq/ocel/pkg/proto/console/v1"
	_ "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	_ "github.com/ocelhq/ocel/pkg/proto/provider/cost/v1"
	_ "github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1"
)

func ocelProtoPaths(t *testing.T) []string {
	t.Helper()

	const root = "../../proto"
	var paths []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || filepath.Ext(path) != ".proto" {
			return err
		}
		relative, err := filepath.Rel(root, path)
		paths = append(paths, filepath.ToSlash(relative))
		return err
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}
	if len(paths) == 0 {
		t.Fatalf("no .proto file under %s, so this test would pass over nothing", root)
	}
	return paths
}

type stringRule struct {
	field      protoreflect.FullName
	pattern    string
	expression string
}

func shippedStringRules(t *testing.T) []stringRule {
	t.Helper()

	var rules []stringRule
	collectFieldRules := func(field protoreflect.FieldDescriptor) {
		fieldRules, ok := proto.GetExtension(field.Options(), validate.E_Field).(*validate.FieldRules)
		if !ok || fieldRules == nil {
			return
		}
		if p := fieldRules.GetString().GetPattern(); p != "" {
			rules = append(rules, stringRule{field: field.FullName(), pattern: p})
		}
		if p := fieldRules.GetRepeated().GetItems().GetString().GetPattern(); p != "" {
			rules = append(rules, stringRule{field: field.FullName(), pattern: p})
		}
		for _, c := range fieldRules.GetCel() {
			rules = append(rules, stringRule{field: field.FullName(), expression: c.GetExpression()})
		}
	}

	var walk func(messages protoreflect.MessageDescriptors)
	walk = func(messages protoreflect.MessageDescriptors) {
		for i := range messages.Len() {
			message := messages.Get(i)
			if messageRules, ok := proto.GetExtension(message.Options(), validate.E_Message).(*validate.MessageRules); ok && messageRules != nil {
				for _, c := range messageRules.GetCel() {
					rules = append(rules, stringRule{field: message.FullName(), expression: c.GetExpression()})
				}
			}
			for j := range message.Fields().Len() {
				collectFieldRules(message.Fields().Get(j))
			}
			walk(message.Messages())
		}
	}

	for _, path := range ocelProtoPaths(t) {
		file, err := protoregistry.GlobalFiles.FindFileByPath(path)
		if err != nil {
			t.Fatalf("%s is not registered: blank-import its generated package here so its buf.validate rules are checked", path)
		}
		walk(file.Messages())
	}
	if len(rules) == 0 {
		t.Fatal("no buf.validate rule was found in any shipped descriptor, so this test would pass over nothing")
	}
	return rules
}

func TestNoShippedValidationRuleNamesAPosixCharacterClass(t *testing.T) {
	for _, rule := range shippedStringRules(t) {
		for _, text := range []string{rule.pattern, rule.expression} {
			if strings.Contains(text, "[:") {
				t.Errorf("%s carries %q, a POSIX class RE2 reads and ECMA-262 (so every generated JSON Schema consumer) refuses", rule.field, text)
			}
		}
	}
}

var controlsAndSpaces = []string{
	"", "/", "/a", "/a/b", "a", "a#b", "#", "/a#b", "a/b", "/a/", "//a", "a b", " ", "/a b",
	"a\x00b", "a\x1fb", "a\x7fb", "a\x20b", "a\x21b", "a\tb", "a\nb", "a\vb", "a\fb", "a\rb",
	"\x00", "\x1f", "\x7f", "/\x00", "/a\x00", "/a\x1f", "/a\x7f", "/a\t", "/a\n",
	"é", "/é", "a\u0085b", "/.", "/..", "/.a", "/..a", "/a/.b", "/a.b", "/...", ".",
	"a@b", "a:b", "a?b", "/a?b", "/a@b", "/a:b", "/up?ready=1", "/up#ready", "/healthz",
}

func TestEachShippedPatternAdmitsExactlyWhatItsRE2PosixClassSpellingAdmits(t *testing.T) {
	const digest = "@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	cases := []struct {
		name    string
		before  string
		after   string
		accepts []string
		refuses []string
	}{
		{
			name:    "a folder of segments with no slash, hash or control character",
			before:  `^(/[^/#[:cntrl:]]+)*$`,
			after:   `^(/[^/#\x00-\x1f\x7f]+)*$`,
			accepts: []string{"", "/a", "/a/b", "/a b", "/é", "/."},
			refuses: []string{"a", "//a", "/a/", "/a#b", "/a\x00", "/a\x1f", "/a\x7f", "/a\t", "/a\n", "/\x00"},
		},
		{
			name:    "a folder of segments naming at least one",
			before:  `^(/[^/#[:cntrl:]]+)+$`,
			after:   `^(/[^/#\x00-\x1f\x7f]+)+$`,
			accepts: []string{"/a", "/a/b", "/a b", "/é"},
			refuses: []string{"", "/", "a", "//a", "/a#b", "/a\x00", "/a\x1f", "/a\x7f", "/a\n"},
		},
		{
			name:    "a name with no hash or control character",
			before:  `^[^#[:cntrl:]]*$`,
			after:   `^[^#\x00-\x1f\x7f]*$`,
			accepts: []string{"", "a", "a b", "a/b", "é", "a\x21b", "a\x20b", "a@b"},
			refuses: []string{"#", "a#b", "a\x00b", "a\x1fb", "a\x7fb", "a\tb", "a\nb", "a\vb", "a\fb", "a\rb"},
		},
		{
			name:    "a variable folder that climbs nowhere",
			before:  `^(/([^/#.[:cntrl:]][^/#[:cntrl:]]*|\.[^/#.[:cntrl:]][^/#[:cntrl:]]*|\.\.[^/#[:cntrl:]]+))*$`,
			after:   `^(/([^/#.\x00-\x1f\x7f][^/#\x00-\x1f\x7f]*|\.[^/#.\x00-\x1f\x7f][^/#\x00-\x1f\x7f]*|\.\.[^/#\x00-\x1f\x7f]+))*$`,
			accepts: []string{"", "/a", "/a/b", "/.a", "/..a", "/...", "/a/.b", "/a.b", "/é"},
			refuses: []string{"/.", "/..", "a", "/a#b", "/a\x00", "/a\x1f", "/a\x7f", "/a\t", "/a\n", "/\x00"},
		},
		{
			name:    "an image pinned by digest",
			before:  `^([^/@:[:space:]]+(:[0-9]+)?/)?[^/@:[:space:]]+(/[^/@:[:space:]]+)*@sha256:[0-9a-f]{64}$`,
			after:   `^([^/@: \t\n\v\f\r]+(:[0-9]+)?/)?[^/@: \t\n\v\f\r]+(/[^/@: \t\n\v\f\r]+)*@sha256:[0-9a-f]{64}$`,
			accepts: []string{"ocel/api" + digest, "registry.example.com:5000/ocel/api" + digest, "localhost:5000/api" + digest, "api" + digest},
			refuses: []string{
				"ocel/api:latest" + digest, "ocel/api", "/ocel/api" + digest, "ocel//api" + digest,
				"ocel/ap i" + digest, "ocel/ap\ti" + digest, "ocel/ap\ni" + digest, "ocel/ap\vi" + digest,
				"ocel/ap\fi" + digest, "ocel/ap\ri" + digest, "ocel/a@pi" + digest, "ocel/api@sha256:short",
			},
		},
		{
			name:    "a health check path off the root",
			before:  `^/[^#?[:space:][:cntrl:]]*$`,
			after:   `^/[^#? \t\n\v\f\r\x00-\x1f\x7f]*$`,
			accepts: []string{"/", "/healthz", "/up/ready", "/up-ready.json", "/a@b", "/a:b", "/é"},
			refuses: []string{"", "healthz", "/up?ready=1", "/up#ready", "/up ready", "/up\tready", "/up\nready", "/up\vready", "/up\fready", "/up\rready", "/up\x00", "/up\x1f", "/up\x7f"},
		},
	}

	shipped := map[string]bool{}
	for _, rule := range shippedStringRules(t) {
		if rule.pattern != "" {
			shipped[rule.pattern] = true
		}
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if !shipped[c.after] {
				t.Errorf("no shipped buf.validate pattern is %q", c.after)
			}
			before, after := regexp.MustCompile(c.before), regexp.MustCompile(c.after)

			inputs := append(append(append([]string{}, controlsAndSpaces...), c.accepts...), c.refuses...)
			for _, input := range inputs {
				if before.MatchString(input) != after.MatchString(input) {
					t.Errorf("%q: before admits %v, after admits %v", input, before.MatchString(input), after.MatchString(input))
				}
			}
			for _, input := range c.accepts {
				if !after.MatchString(input) {
					t.Errorf("after refuses %q, want it admitted", input)
				}
			}
			for _, input := range c.refuses {
				if after.MatchString(input) {
					t.Errorf("after admits %q, want it refused", input)
				}
			}
		})
	}
}

func TestAControlCharacterRefusesAKeyAndADescriptionThroughProtovalidate(t *testing.T) {
	cases := []struct {
		name    string
		message func(text string) proto.Message
	}{
		{"a presigned file key", func(text string) proto.Message { return &bucketv1.PresignFile{Key: text} }},
		{"a variable description", func(text string) proto.Message { return &resourcesv1.VariableDefinition{Key: "K", Description: text} }},
		{"a group description", func(text string) proto.Message { return &resourcesv1.GroupDefinition{Key: "K", Description: text} }},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			for _, text := range []string{"a/b", "a b", "é", "a-b.c"} {
				if err := protovalidate.Validate(c.message(text)); err != nil {
					t.Errorf("%q refused: %v", text, err)
				}
			}
			for _, text := range []string{"a\x00b", "a\x1fb", "a\x7fb", "a\tb", "a\nb", "a\rb"} {
				if err := protovalidate.Validate(c.message(text)); err == nil {
					t.Errorf("%q admitted, want a control character refused", text)
				}
			}
		})
	}
}
