package docker

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/dockerpolicy"
)

const engineAnsweringNetworkLs = `#!/bin/sh
[ "$1" = network ] || exit 0
case "$2" in
ls)
  case " $* " in
  *" --quiet "*|*" -q "*) printf '370d5327ba7b\n' ;;
  *"{{.Name}}"*) printf 'ocel-production-shop\n' ;;
  *) printf 'NETWORK ID     NAME                   DRIVER    SCOPE\n370d5327ba7b   ocel-production-shop   bridge    local\n' ;;
  esac
  ;;
connect)
  shift 2
  printf '%s\n' "$*" >> "$JOINED"
  ;;
esac
`

type noContainerIsOcels struct{}

func (noContainerIsOcels) IsOcels(context.Context, string) (bool, error) { return false, nil }

func (noContainerIsOcels) IsHostBound(context.Context, string) (bool, error) { return false, nil }

func TestTheSwitchboardRejoinsEachProjectNetworkByANameTheCheckedDockerLetsThrough(t *testing.T) {
	t.Parallel()

	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(engineAnsweringNetworkLs), 0o755); err != nil {
		t.Fatal(err)
	}
	joined := filepath.Join(t.TempDir(), "joined")
	rejoin := exec.Command("sh", "-c", rejoining(boxapi.Switchboard))
	rejoin.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "JOINED="+joined)
	if said, err := rejoin.CombinedOutput(); err != nil {
		t.Fatalf("the rejoin ran into %v:\n%s", err, said)
	}
	connects, err := os.ReadFile(joined)
	if err != nil {
		t.Fatalf("the rejoin connected the switchboard to nothing on an engine with one project network: %v", err)
	}

	policy := dockerpolicy.Policy{Inspect: noContainerIsOcels{}}
	for connect := range strings.Lines(string(connects)) {
		argv := append([]string{"network", "connect"}, strings.Fields(connect)...)
		inv, err := policy.Open(context.Background(), argv)
		inv.Close()
		if err != nil {
			t.Errorf("the switchboard rejoins with `docker %s`, which the checked docker the deploy login runs refuses: %v", strings.Join(argv, " "), err)
		}
	}
}
