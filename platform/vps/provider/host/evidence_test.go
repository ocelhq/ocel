package host

import (
	"slices"
	"strings"
	"testing"
)

func inspecting() map[string]string {
	return map[string]string{
		"what a release reads to see if the app is already serving": servingCommand(physical),
		"what a release that fell over captures as evidence":        stateCommand(physical),
		"what a release that fell over captures as logs":            logCommand(physical),
		"what a proxy that did not come up reports":                 containerRising(3),
		"what a bootstrap probes the proxy with":                    containerProbe(),
		"what a preflight reads the proxy's state with":             stateCommand(ProxyContainer),
	}
}

func TestEveryInspectOnTheEvidencePathNamesTheFieldsItReads(t *testing.T) {
	t.Parallel()

	read := 0
	for what, command := range inspecting() {
		for at := 0; ; {
			found := strings.Index(command[at:], "inspect")
			if found < 0 {
				break
			}
			at += found + len("inspect")
			read++
			line, _, _ := strings.Cut(command[at:], "\n")
			if !strings.Contains(line, "--format") {
				t.Errorf("%s runs `inspect%s`, which prints the whole of what it inspects — a container's environment among it — into a deploy's output",
					what, line)
			}
		}
	}
	if read < len(inspecting()) {
		t.Fatalf("this guard read %d inspects across %d commands, so it is passing over commands it never looked at", read, len(inspecting()))
	}
}

func TestNoInspectOnTheEvidencePathCanReachTheEnvironmentItWasHanded(t *testing.T) {
	t.Parallel()

	held := inspecting()
	held["the fact template a bootstrap compares the proxy against"] = ProxyFactTemplate
	held["what a preflight reads the disk headroom with"] = headroomCommand([]string{"ocel-shop-web"})
	for what, command := range held {
		for _, leak := range []string{".Config.Env", ".Env}}", "{{json .}}", "--format '{{.}}'"} {
			if strings.Contains(command, leak) {
				t.Errorf("%s reads %q, and %s prints every value the container was handed", what, command, leak)
			}
		}
		for at := 0; ; {
			found := strings.Index(command[at:], ".Config")
			if found < 0 {
				break
			}
			at += found + len(".Config")
			if !strings.HasPrefix(command[at:], ".Labels") && !strings.HasPrefix(command[at:], ".Image") {
				t.Errorf("%s reads %q and reaches into the container's configuration past the labels and the image it names", what, command)
			}
		}
	}
}

func inspectRosters() map[string][]string {
	return map[string][]string{
		"docker inspect":         {"containerProbe", "containerRising", "servingCommand", "stateCommand"},
		"docker network inspect": {"command", "networkCommand", "networkProbe"},
		"docker image inspect":   {"imageHeld"},
	}
}

func TestEveryInspectThisPackageRunsIsHeldToTheSelectorRules(t *testing.T) {
	t.Parallel()

	for marker, roster := range inspectRosters() {
		rendered(t, marker, roster,
			"an inspect rendered somewhere this bench does not read is held to none of the rules in this file, and a bare one prints every value a container was handed")
	}
}

func TestEveryInspectThisPackageRunsIsOneOfTheFlavoursThoseRostersCover(t *testing.T) {
	t.Parallel()

	var covered []string
	for marker := range inspectRosters() {
		covered = append(covered, sites(t, marker)...)
	}
	slices.Sort(covered)
	covered = slices.Compact(covered)

	found := sites(t, "inspect")
	if len(found) == 0 {
		t.Fatal("no source in this package renders an inspect at all, so the partition this bench holds them to proves nothing")
	}
	if !slices.Equal(found, covered) {
		t.Errorf("this package renders inspects in %v and the rosters above reach %v: an inspect of a kind no roster names is held to none of the rules in this file, and the per-marker rosters cannot see that it exists",
			found, covered)
	}
}

func TestNoNetworkInspectCanNameAContainerToInspectInstead(t *testing.T) {
	t.Parallel()

	networking := map[string]string{
		"what a bootstrap creates the proxy network with": networkCommand(),
		"what a bootstrap probes the proxy network with":  networkProbe(),
		"what a destroy removes the proxy network with":   removal{kind: KindNetwork, path: ProxyNetwork}.command(),
	}
	if len(networking) != len(inspectRosters()["docker network inspect"]) {
		t.Fatalf("this bench reads %d network inspects and the package renders %d, so what it does not read is held to nothing",
			len(networking), len(inspectRosters()["docker network inspect"]))
	}
	for what, command := range networking {
		if !strings.Contains(command, "docker network inspect "+quoted(ProxyNetwork)) {
			t.Fatalf("%s runs %q, which inspects no network by name, so this guard is reading a command that does nothing", what, command)
		}
		if strings.Contains(command, "--type container") || strings.Contains(command, ProxyContainer) {
			t.Errorf("%s runs %q and names a container: a network inspect is exempt from naming its fields because a network carries no value a container was handed, and one that reaches a container is not",
				what, command)
		}
	}
}

func TestTheLogsARefusalQuotesAreBounded(t *testing.T) {
	t.Parallel()

	command := logCommand(physical)
	if !strings.Contains(command, "--tail "+appLogTail) {
		t.Errorf("a refusal quotes %q, and an unbounded log is a deploy's whole output", command)
	}
}

func expiring() map[string]string {
	return map[string]string{
		"what doctor reads a served leaf with": words(helperCommand("leaf", "shop.example.com")),
		"what a pinned pair is read off":       "cat " + quoted(PinCertificate(ProxyPins+"/wildcard")),
	}
}

func TestNothingThisPackageReadsAnExpiryOffReachesTheProxysDataDirectory(t *testing.T) {
	t.Parallel()

	named := 0
	for what, command := range expiring() {
		if !strings.Contains(command, "shop.example.com") && !strings.Contains(command, ProxyPins) {
			t.Fatalf("%s runs %q, which names neither the hostname nor %s, so this guard is reading a command that does nothing", what, command, ProxyPins)
		}
		named++
		if strings.Contains(command, ProxyData) {
			t.Errorf("%s runs %q and reaches into %s: caddy's storage layout is undocumented as an interface and is what a version bump rearranges, and an expiry is read off the served leaf instead",
				what, command, ProxyData)
		}
	}
	if named != len(expiring()) {
		t.Fatalf("this guard read %d of %d commands", named, len(expiring()))
	}
}

func TestForgettingAPairIsTheOneHelperVerbThatSpendsTheProxysDataDirectory(t *testing.T) {
	t.Parallel()

	forgetting := words(helperCommand("forget", "shop.example.com"))
	if !strings.Contains(forgetting, "forget") || !strings.Contains(forgetting, "shop.example.com") {
		t.Fatalf("a pair is forgotten by %q, which names neither the verb nor a hostname, so the exemption this bench states is read over a window that holds nothing", forgetting)
	}
	for what := range expiring() {
		if what == "what a hostname's pair is forgotten by" {
			t.Fatalf("%q is read as an expiry, and it is the one verb that spends %s: the rule above would then forbid what the code must do", what, ProxyData)
		}
	}
}
