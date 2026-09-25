package host

import (
	"slices"
	"strings"
	"testing"
)

func capabilitiesAdded(argv []string) []string {
	var added []string
	for at, arg := range argv {
		if arg == "--cap-add" && at+1 < len(argv) {
			added = append(added, argv[at+1])
		}
	}
	return added
}

func TestAnAppContainerIsConfinedAndItsLogIsRotated(t *testing.T) {
	t.Parallel()

	argv := containerRun(valued(), handedTo(valued()))
	command := words(argv)
	for what, wanted := range map[string]string{
		"every capability dropped before any is handed back": quoted("--cap-drop") + " " + quoted("ALL"),
		"no privilege gained past what it started with":      quoted("--security-opt") + " " + quoted(noNewPrivileges),
		"a ceiling on the processes it may fork":             quoted("--pids-limit") + " " + quoted(pidsLimit),
		"a log driver that rotates":                          quoted("--log-driver") + " " + quoted(logDriver),
		"a size each log file is rotated at":                 quoted("--log-opt") + " " + quoted("max-size="+logMaxSize),
		"a count of log files kept":                          quoted("--log-opt") + " " + quoted("max-file="+logMaxFiles),
	} {
		if !strings.Contains(command, wanted) {
			t.Errorf("standing a container up runs %q, which carries no %s (%s)", command, what, wanted)
		}
	}
	added := capabilitiesAdded(argv)
	if !slices.Equal(added, appCapabilities) {
		t.Errorf("the container is handed back %v, want %v", added, appCapabilities)
	}
	for _, refused := range []string{"NET_RAW", "SYS_ADMIN", "MKNOD", "SYS_CHROOT", "AUDIT_WRITE", "SETFCAP", "NET_ADMIN", "SYS_PTRACE"} {
		if slices.Contains(added, refused) {
			t.Errorf("the container is handed %s back, and nothing an app serving http needs is behind it", refused)
		}
	}
	if slices.Index(argv, "--cap-drop") > slices.Index(argv, "--cap-add") {
		t.Error("a capability is added before ALL are dropped, and docker applies the drop after the add either way; keep the order readable")
	}
}

func TestTheProxyIsConfinedToBindingItsPortsAndItsLogIsRotated(t *testing.T) {
	t.Parallel()

	argv := frontProxy().run()
	command := words(argv)
	if strings.Contains(command, quoted(noNewPrivileges)) {
		t.Errorf("the proxy is run under %s, and its image carries cap_net_bind_service=ep as a file capability on the binary, which the kernel refuses to exec once no_new_privs is set:\n%s", noNewPrivileges, command)
	}
	for _, wanted := range []string{
		quoted("--cap-drop") + " " + quoted("ALL"),
		quoted("--pids-limit") + " " + quoted(pidsLimit),
		quoted("--log-driver") + " " + quoted(logDriver),
		quoted("--log-opt") + " " + quoted("max-size="+logMaxSize),
		quoted("--log-opt") + " " + quoted("max-file="+logMaxFiles),
	} {
		if !strings.Contains(command, wanted) {
			t.Errorf("the proxy is run without %s:\n%s", wanted, command)
		}
	}
	if added := capabilitiesAdded(argv); !slices.Equal(added, []string{"NET_BIND_SERVICE", "DAC_OVERRIDE", "DAC_READ_SEARCH"}) {
		t.Errorf("the proxy is handed back %v, want the three it uses: it binds 80 and 443 as root, reads a config and a socket the deploy login owns, and needs nothing else the kernel gates", added)
	}
}

func TestTheDiskBudgetCountsTheLogEachIncomingContainerMayKeep(t *testing.T) {
	t.Parallel()

	room, err := readHeadroom("root=/var/lib/docker\nfree=1024\nrepo=ocel-shop-web\nsize=300MB\nrepo=ocel-shop-api\nsize=300MB\n")
	if err != nil {
		t.Fatalf("readHeadroom() = %v", err)
	}
	images := room.Repos["ocel-shop-web"].Needs() + room.Repos["ocel-shop-api"].Needs()
	if room.Needs() != images+2*LogCeiling {
		t.Errorf("Needs() = %d, want %d: the engine rotates each container's log at %s files of %s, and that ceiling is disk the deploy fills after the transfer the budget guards", room.Needs(), images+2*LogCeiling, logMaxFiles, logMaxSize)
	}
	if want := int64(20 * 5 << 20); LogCeiling != want {
		t.Errorf("LogCeiling = %d, want %d: max-size %s times max-file %s, in the binary units docker reads sizes in", LogCeiling, want, logMaxSize, logMaxFiles)
	}
	if !strings.Contains(arithmetic(room), sized(LogCeiling)) {
		t.Errorf("the refusal's arithmetic %q never names the log ceiling it added", arithmetic(room))
	}
}
