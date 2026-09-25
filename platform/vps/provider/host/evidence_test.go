package host

import (
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/platform/vps/provider/proxy/caddy"
)

func inspecting() map[string]string {
	return map[string]string{
		"what a release reads to see if the app is already serving":              servingCommand(physical),
		"what a release that fell over captures as evidence":                     stateCommand(physical),
		"what a release that fell over captures as logs":                         logCommand(physical),
		"what a proxy that did not come up reports":                              frontProxy().rising(3),
		"what a switchboard that did not come up reports":                        switchboardStanding(nil, Front{}).rising(3),
		"what a bootstrap probes the proxy with":                                 frontProxy().probe(),
		"what a preflight reads the proxy's state with":                          stateCommand(caddy.Container),
		"what a release reads to tell a stopped retiree from one still draining": runningCommand([]string{retiring}),
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
	held["the fact template a bootstrap compares the proxy against"] = ContainerFactTemplate
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
		"docker inspect":         {"probe", "rising", "runningCommand", "servingCommand", "stateCommand"},
		"docker network inspect": {"command", "networkCommand", "networkCreating", "networkForgetting", "networkProbe", "networkStanding"},
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

	project := AppNetwork(valued().Class, valued().Project)
	networking := map[string]struct{ command, network string }{
		"what a bootstrap creates the proxy network with":     {networkCommand(), ProxyNetwork},
		"what a bootstrap probes the proxy network with":      {networkProbe(), ProxyNetwork},
		"what a destroy removes the proxy network with":       {removal{kind: KindNetwork, path: ProxyNetwork}.command(), ProxyNetwork},
		"what a deploy puts a project's network up with":      {networkStanding(valued().Class, valued().Project), project},
		"what a resource puts a project's network up with":    {networkCreating(valued().Class, valued().Project), project},
		"what a teardown takes a project's network down with": {networkForgetting(valued().Class, valued().Project), project},
	}
	if len(networking) != len(inspectRosters()["docker network inspect"]) {
		t.Fatalf("this bench reads %d network inspects and the package renders %d, so what it does not read is held to nothing",
			len(networking), len(inspectRosters()["docker network inspect"]))
	}
	for what, held := range networking {
		inspects := 0
		for line := range strings.Lines(held.command) {
			for _, ask := range strings.Split(line, "docker network inspect ")[1:] {
				inspects++
				ask, _, _ = strings.Cut(ask, "|")
				if !strings.Contains(ask, quoted(held.network)) {
					t.Errorf("%s runs %q, which inspects something other than %s by name", what, strings.TrimSpace(line), held.network)
				}
				if strings.Contains(ask, "--type container") || strings.Contains(ask, caddy.Container) || strings.Contains(ask, SwitchboardContainer) {
					t.Errorf("%s runs %q and inspects a container: a network inspect is exempt from naming its fields because a network carries no value a container was handed, and one that reaches a container is not",
						what, strings.TrimSpace(line))
				}
				if format, formatted := strings.CutPrefix(ask, "--format "); formatted && !strings.HasPrefix(format, quoted(membersFormat)) {
					t.Errorf("%s runs %q with a format other than the container names on the network, and a network inspect that prints more prints what those containers hold",
						what, strings.TrimSpace(line))
				}
			}
		}
		if inspects == 0 {
			t.Fatalf("%s runs %q, which inspects no network at all, so this guard is reading a command that does nothing", what, held.command)
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
		"what doctor reads a served leaf with": words([]string{SwitchboardBinary, "leaf", "shop.example.com"}),
		"what a pinned pair is read off":       "cat " + quoted(caddy.PinCertificate(caddy.PinsDir+"/wildcard")),
	}
}

func TestNothingThisPackageReadsAnExpiryOffReachesTheProxysDataDirectory(t *testing.T) {
	t.Parallel()

	named := 0
	for what, command := range expiring() {
		if !strings.Contains(command, "shop.example.com") && !strings.Contains(command, caddy.PinsDir) {
			t.Fatalf("%s runs %q, which names neither the hostname nor %s, so this guard is reading a command that does nothing", what, command, caddy.PinsDir)
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
