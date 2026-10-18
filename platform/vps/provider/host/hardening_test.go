package host

import (
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/docker"
	"github.com/ocelhq/ocel/platform/vps/provider/routing"
	"github.com/ocelhq/ocel/platform/vps/provider/shell"
	"github.com/ocelhq/ocel/platform/vps/provider/tunnel"
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
	command := shell.Words(argv)
	for what, wanted := range map[string]string{
		"every capability dropped before any is handed back": shell.Quote("--cap-drop") + " " + shell.Quote("ALL"),
		"no privilege gained past what it started with":      shell.Quote("--security-opt") + " " + shell.Quote(docker.NoNewPrivileges),
		"a ceiling on the processes it may fork":             shell.Quote("--pids-limit") + " " + shell.Quote(docker.PidsLimit),
		"a log driver that rotates":                          shell.Quote("--log-driver") + " " + shell.Quote(docker.LogDriver),
		"a size each log file is rotated at":                 shell.Quote("--log-opt") + " " + shell.Quote("max-size="+docker.LogMaxSize),
		"a count of log files kept":                          shell.Quote("--log-opt") + " " + shell.Quote("max-file="+docker.LogMaxFiles),
	} {
		if !strings.Contains(command, wanted) {
			t.Errorf("starting a container runs %q, which includes no %s (%s)", command, what, wanted)
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

	argv := frontProxy().Run()
	command := shell.Words(argv)
	if strings.Contains(command, shell.Quote(docker.NoNewPrivileges)) {
		t.Errorf("the proxy is run under %s, and its image ships cap_net_bind_service=ep as a file capability on the binary, which the kernel refuses to exec once no_new_privs is set:\n%s", docker.NoNewPrivileges, command)
	}
	for _, wanted := range []string{
		shell.Quote("--cap-drop") + " " + shell.Quote("ALL"),
		shell.Quote("--pids-limit") + " " + shell.Quote(docker.PidsLimit),
		shell.Quote("--log-driver") + " " + shell.Quote(docker.LogDriver),
		shell.Quote("--log-opt") + " " + shell.Quote("max-size="+docker.LogMaxSize),
		shell.Quote("--log-opt") + " " + shell.Quote("max-file="+docker.LogMaxFiles),
	} {
		if !strings.Contains(command, wanted) {
			t.Errorf("the proxy is run without %s:\n%s", wanted, command)
		}
	}
	if added := capabilitiesAdded(argv); !slices.Equal(added, []string{"NET_BIND_SERVICE", "DAC_OVERRIDE"}) {
		t.Errorf("the proxy is handed back %v, want the two it uses: it binds 80 and 443 as root, reads a config and a socket the deploy login owns, and needs nothing else the kernel gates", added)
	}
}

func TestNoContainerOcelRunsMayOpenHostFilesByHandle(t *testing.T) {
	t.Parallel()

	for what, argv := range map[string][]string{
		"the proxy":          frontProxy().Run(),
		"the switchboard":    switchboardBox(nil, Front{}).Run(),
		"a placement":        routing.Placement("/data/proxy", "origin-group"),
		"an app":             containerRun(valued(), handedTo(valued())),
		"a resource":         resourceRun(resourced(), "0123456789ab", "/tmp/handed.env"),
		"the tunnel":         tunnel.Service(boxapi.Tunnel{Edge: "cloudflare", ID: "5a6b7c8d-1"}, newConnector()).Run(),
		"the tunnel's token": tunnel.TokenArgv("place-secret", "ocel-203-0-113-10-0a1b2c3d"),
	} {
		if slices.Contains(capabilitiesAdded(argv), "DAC_READ_SEARCH") {
			t.Errorf("%s is handed DAC_READ_SEARCH, which docker's seccomp profile answers by allowing open_by_handle_at, so a process in it reads any file on the filesystem one of its bind mounts sits on, seal keys among them", what)
		}
	}
}

func TestTheDiskBudgetCountsTheLogEachIncomingContainerMayKeep(t *testing.T) {
	t.Parallel()

	room, err := readHeadroom("root=/var/lib/docker\nfree=1024\nrepo=ocel-shop-web\nsize=300MB\nrepo=ocel-shop-api\nsize=300MB\n")
	if err != nil {
		t.Fatalf("readHeadroom() = %v", err)
	}
	images := room.Repos["ocel-shop-web"].Needs() + room.Repos["ocel-shop-api"].Needs()
	if room.Needs() != images+2*docker.LogCeiling {
		t.Errorf("Needs() = %d, want %d: the engine rotates each container's log at %s files of %s, and that ceiling is disk the deploy fills after the transfer the budget guards", room.Needs(), images+2*docker.LogCeiling, docker.LogMaxFiles, docker.LogMaxSize)
	}
	if want := int64(20 * 5 << 20); docker.LogCeiling != want {
		t.Errorf("LogCeiling = %d, want %d: max-size %s times max-file %s, in the binary units docker reads sizes in", docker.LogCeiling, want, docker.LogMaxSize, docker.LogMaxFiles)
	}
	if !strings.Contains(arithmetic(room), sized(docker.LogCeiling)) {
		t.Errorf("the refusal's arithmetic %q never names the log ceiling it added", arithmetic(room))
	}
}
