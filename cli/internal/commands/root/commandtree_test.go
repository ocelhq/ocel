package root

import (
	"bytes"
	"encoding/json"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/ocelhq/ocel/cli/internal/commands"
	helpv1 "github.com/ocelhq/ocel/pkg/proto/cli/help/v1"
)

func visibleCommands(root *cobra.Command) []*cobra.Command {
	var found []*cobra.Command
	var walk func(cmd *cobra.Command)
	walk = func(cmd *cobra.Command) {
		for _, sub := range cmd.Commands() {
			if sub.Hidden {
				continue
			}
			found = append(found, sub)
			walk(sub)
		}
	}
	walk(root)
	return found
}

func commandPath(cmd *cobra.Command) string {
	return strings.TrimPrefix(cmd.CommandPath(), cmd.Root().Name()+" ")
}

func describedCommands(t *testing.T, stdout string) map[string]*helpv1.Command {
	t.Helper()
	tree := &helpv1.CommandTree{}
	if err := protojson.Unmarshal([]byte(stdout), tree); err != nil {
		t.Fatalf("stdout is not a protojson CommandTree: %v\n%s", err, stdout)
	}
	byPath := map[string]*helpv1.Command{}
	for _, command := range tree.GetCommands() {
		byPath[command.GetPath()] = command
	}
	return byPath
}

func describedFlag(command *helpv1.Command, name string) *helpv1.Flag {
	for _, flag := range command.GetFlags() {
		if flag.GetName() == name {
			return flag
		}
	}
	return nil
}

func TestEveryVisibleCommandDeclaresWhetherItMutates(t *testing.T) {
	for _, cmd := range visibleCommands(newCommand().root) {
		if _, declared := commands.FindMutation(cmd); !declared {
			t.Errorf("ocel %s does not declare whether it mutates, want commands.DeclareMutating or commands.DeclareReadOnly", commandPath(cmd))
		}
	}
}

func TestHelpJSONDescribesEveryVisibleCommand(t *testing.T) {
	stdout, _ := executeRoot(t, "help", "--json")

	described := describedCommands(t, stdout)

	for _, cmd := range visibleCommands(newCommand().root) {
		if described[commandPath(cmd)] == nil {
			t.Errorf("ocel help --json does not describe %q", commandPath(cmd))
		}
	}
	if len(described) != len(visibleCommands(newCommand().root)) {
		t.Errorf("ocel help --json describes %d commands, want the %d visible ones", len(described), len(visibleCommands(newCommand().root)))
	}
}

func TestHelpJSONLeavesOutHiddenCommandsAndHiddenFlags(t *testing.T) {
	ocel := newCommand()
	secret := &cobra.Command{Use: "secret", Hidden: true, RunE: func(*cobra.Command, []string) error { return nil }}
	commands.DeclareReadOnly(secret)
	ocel.root.AddCommand(secret)
	visible := commands.DeclareReadOnly(&cobra.Command{Use: "visible", RunE: func(*cobra.Command, []string) error { return nil }})
	visible.Flags().String("open", "", "A flag anyone may pass")
	visible.Flags().String("internal", "", "A flag only maintainers pass")
	if err := visible.Flags().MarkHidden("internal"); err != nil {
		t.Fatal(err)
	}
	ocel.root.AddCommand(visible)
	var stdout bytes.Buffer
	ocel.root.SetArgs([]string{"help", "--json"})
	ocel.root.SetOut(&stdout)
	if err := ocel.execute(); err != nil {
		t.Fatal(err)
	}

	described := describedCommands(t, stdout.String())

	for _, hidden := range []string{"secret", "help", "completion"} {
		if described[hidden] != nil {
			t.Errorf("ocel help --json describes the hidden command %q", hidden)
		}
	}
	if describedFlag(described["visible"], "internal") != nil {
		t.Errorf("ocel help --json describes a hidden flag")
	}
	if describedFlag(described["visible"], "open") == nil {
		t.Errorf("ocel help --json leaves out the visible flag --open")
	}
}

func TestHelpJSONIsByteIdenticalAcrossRuns(t *testing.T) {
	first, _ := executeRoot(t, "help", "--json")
	second, _ := executeRoot(t, "help", "--json")

	if first != second {
		t.Errorf("two runs of ocel help --json differ")
	}
}

func TestHelpJSONSortsCommandsByPathAndFlagsByName(t *testing.T) {
	stdout, _ := executeRoot(t, "help", "--json")
	tree := &helpv1.CommandTree{}
	if err := protojson.Unmarshal([]byte(stdout), tree); err != nil {
		t.Fatal(err)
	}

	var paths []string
	for _, command := range tree.GetCommands() {
		paths = append(paths, command.GetPath())
		var names []string
		for _, flag := range command.GetFlags() {
			names = append(names, flag.GetName())
		}
		if !sort.StringsAreSorted(names) {
			t.Errorf("flags of %q = %v, want them sorted by name", command.GetPath(), names)
		}
	}
	if !sort.StringsAreSorted(paths) {
		t.Errorf("command paths = %v, want them sorted", paths)
	}
}

func TestHelpJSONIsTheBareDocumentWithNoEnvelopeOrTrailingText(t *testing.T) {
	stdout, stderr := executeRoot(t, "help", "--json")

	var document map[string]json.RawMessage
	if err := json.Unmarshal([]byte(stdout), &document); err != nil {
		t.Fatalf("stdout is not one JSON document: %v", err)
	}
	if _, ok := document["commands"]; !ok || len(document) != 1 {
		t.Errorf("document keys = %v, want only the commands", document)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want nothing", stderr)
	}
}

func TestHelpJSONNamesFieldsInSnakeCaseAndSpellsOutFalse(t *testing.T) {
	stdout, _ := executeRoot(t, "help", "env", "ls", "--json")

	var document struct {
		Commands []map[string]any `json:"commands"`
	}
	if err := json.Unmarshal([]byte(stdout), &document); err != nil {
		t.Fatal(err)
	}

	if len(document.Commands) != 1 {
		t.Fatalf("commands = %d, want only env ls", len(document.Commands))
	}
	for _, key := range []string{"path", "short", "long", "usage", "arguments", "aliases", "flags", "prints_data", "mutates", "confirms"} {
		if _, ok := document.Commands[0][key]; !ok {
			t.Errorf("env ls has no %q key: %v", key, document.Commands[0])
		}
	}
	if document.Commands[0]["mutates"] != false {
		t.Errorf("mutates = %v, want an explicit false", document.Commands[0]["mutates"])
	}
}

func TestHelpJSONSaysWhichCommandsMutate(t *testing.T) {
	stdout, _ := executeRoot(t, "help", "--json")
	described := describedCommands(t, stdout)

	for path, want := range map[string]bool{
		"deploy":             true,
		"destroy production": true,
		"env set":            true,
		"env ls":             false,
		"doctor":             false,
	} {
		if got := described[path].GetMutates(); got != want {
			t.Errorf("%s mutates = %v, want %v", path, got, want)
		}
	}
}

func TestHelpJSONSaysWhichCommandsPrintData(t *testing.T) {
	stdout, _ := executeRoot(t, "help", "--json")
	described := describedCommands(t, stdout)

	for path, want := range map[string]bool{
		"env ls":      true,
		"doctor":      true,
		"logs":        true,
		"deploy":      false,
		"preview up":  false,
		"dev":         false,
		"run":         false,
		"login":       false,
		"preview ls":  true,
		"env set":     true,
		"bindings ls": true,
	} {
		if got := described[path].GetPrintsData(); got != want {
			t.Errorf("%s prints_data = %v, want %v", path, got, want)
		}
	}
}

func TestHelpJSONSaysWhichCommandsConfirm(t *testing.T) {
	stdout, _ := executeRoot(t, "help", "--json")
	described := describedCommands(t, stdout)

	for path, want := range map[string]bool{
		"deploy":             true,
		"destroy production": true,
		"bootstrap preview":  true,
		"env get":            true,
		"preview rm":         true,
		"env ls":             false,
		"doctor":             false,
		"destroy":            false,
	} {
		if got := described[path].GetConfirms(); got != want {
			t.Errorf("%s confirms = %v, want %v", path, got, want)
		}
	}
}

func TestHelpJSONDescribesAFlagByNameShorthandTypeDefaultAndDescription(t *testing.T) {
	stdout, _ := executeRoot(t, "help", "--json")
	deploy := describedCommands(t, stdout)["deploy"]

	yes := describedFlag(deploy, "yes")
	if yes == nil {
		t.Fatal("deploy has no --yes")
	}
	if yes.GetShorthand() != "y" || yes.GetType() != "bool" || yes.GetDefault() != "false" || yes.GetDescription() != commands.YesUsage || yes.GetPersistent() {
		t.Errorf("deploy --yes = %v, want shorthand y, type bool, default false, description %q, not persistent", yes, commands.YesUsage)
	}
}

func TestHelpJSONDescribesTheDescriptionOfAFlagWithoutItsBackquotes(t *testing.T) {
	stdout, _ := executeRoot(t, "help", "--json")
	config := describedFlag(describedCommands(t, stdout)["deploy"], "config")

	if strings.Contains(config.GetDescription(), "`") {
		t.Errorf("--config description = %q, want no backquotes", config.GetDescription())
	}
}

func TestHelpJSONReportsTheRootsPersistentFlagsOnEveryCommand(t *testing.T) {
	stdout, _ := executeRoot(t, "help", "--json")
	described := describedCommands(t, stdout)

	for path, command := range described {
		for _, name := range []string{"json", "verbose", "config"} {
			flag := describedFlag(command, name)
			if flag == nil || !flag.GetPersistent() {
				t.Errorf("%s does not report the persistent --%s: %v", path, name, flag)
			}
		}
		if describedFlag(command, "help") != nil {
			t.Errorf("%s reports cobra's own --help flag", path)
		}
	}
	if verbose := describedFlag(described["deploy"], "verbose"); verbose.GetShorthand() != "v" {
		t.Errorf("--verbose shorthand = %q, want v", verbose.GetShorthand())
	}
}

func TestHelpJSONDescribesTheArgumentsOfACommandFromItsUsage(t *testing.T) {
	stdout, _ := executeRoot(t, "help", "--json")
	described := describedCommands(t, stdout)

	for path, want := range map[string][]*helpv1.Argument{
		"deploy":  nil,
		"link":    {{Name: "project"}},
		"env get": {{Name: "KEY", Required: true}},
		"env set": {{Name: "KEY=VALUE", Required: true, Variadic: true}},
		"logs":    {{Name: "app", Variadic: true}},
		"dev":     {{Name: "command", Required: true}, {Name: "args", Variadic: true}},
		"env":     nil,
	} {
		got := described[path].GetArguments()
		if len(got) != len(want) {
			t.Errorf("%s arguments = %v, want %v", path, got, want)
			continue
		}
		for i := range want {
			if got[i].GetName() != want[i].GetName() || got[i].GetRequired() != want[i].GetRequired() || got[i].GetVariadic() != want[i].GetVariadic() {
				t.Errorf("%s argument %d = %v, want %v", path, i, got[i], want[i])
			}
		}
	}
}

func TestHelpJSONDescribesAliasesUsageAndDescriptions(t *testing.T) {
	stdout, _ := executeRoot(t, "help", "--json")
	described := describedCommands(t, stdout)

	production := described["destroy production"]
	if !slices.Equal(production.GetAliases(), []string{"prod"}) {
		t.Errorf("destroy production aliases = %v, want [prod]", production.GetAliases())
	}
	if want := "ocel destroy production [flags]"; production.GetUsage() != want {
		t.Errorf("destroy production usage = %q, want %q", production.GetUsage(), want)
	}
	if production.GetShort() == "" || production.GetLong() == "" {
		t.Errorf("destroy production short = %q, long = %q, want both", production.GetShort(), production.GetLong())
	}
}

func TestHelpJSONForACommandDescribesItAndItsSubcommands(t *testing.T) {
	stdout, _ := executeRoot(t, "help", "env", "--json")

	described := describedCommands(t, stdout)

	if described["env"] == nil || described["env set"] == nil || described["deploy"] != nil {
		var paths []string
		for path := range described {
			paths = append(paths, path)
		}
		t.Errorf("ocel help env --json describes %v, want env and what is under it", paths)
	}
}

func TestHelpJSONForAnUnknownCommandIsAnError(t *testing.T) {
	ocel := newCommand()
	ocel.root.SetArgs([]string{"help", "nonesuch", "--json"})
	var stdout bytes.Buffer
	ocel.root.SetOut(&stdout)

	err := ocel.execute()

	if err == nil || !strings.Contains(err.Error(), "nonesuch") {
		t.Errorf("err = %v, want it to name the unknown command", err)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want nothing", stdout.String())
	}
}

func TestOCELJSONMakesHelpPrintTheCommandTree(t *testing.T) {
	t.Setenv("OCEL_JSON", "1")

	stdout, _ := executeRoot(t, "help")

	if described := describedCommands(t, stdout); described["deploy"] == nil {
		t.Errorf("ocel help with OCEL_JSON=1 does not describe deploy")
	}
}

func TestHelpWithoutJSONStaysTheHumanUsage(t *testing.T) {
	helped, _ := executeRoot(t, "help", "deploy")
	flagged, _ := executeRoot(t, "deploy", "--help")

	if helped != flagged || !strings.Contains(helped, "USAGE") || strings.HasPrefix(strings.TrimSpace(helped), "{") {
		t.Errorf("ocel help deploy = %q, want the same human usage as ocel deploy --help = %q", helped, flagged)
	}
}
