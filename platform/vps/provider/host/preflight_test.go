package host

import (
	"os"
	"strings"
	"testing"
)

const headroomSaid = `root=/var/lib/docker
free=1024
repo=ocel-shop-web
size=100
size=300
size=200
`

func TestTheKeepWindowIsTheOneTheHelperOnTheBoxEnforces(t *testing.T) {
	t.Parallel()

	read, err := os.ReadFile("releases.sh")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(read), "\nkeep=3\n") {
		t.Fatalf("releases.sh does not set keep=3, so %d is arithmetic against a window nothing on the box enforces", KeepWindow)
	}
	if KeepWindow != 3 {
		t.Errorf("KeepWindow = %d, want the 3 releases.sh keeps", KeepWindow)
	}
}

func TestHeadroomIsReadOffTheLargestImageHeldAndTheSlotsLeft(t *testing.T) {
	t.Parallel()

	room, err := readHeadroom(headroomSaid)
	if err != nil {
		t.Fatalf("readHeadroom() = %v", err)
	}
	if room.Root != "/var/lib/docker" || room.Free != 1024*1024 {
		t.Fatalf("readHeadroom() = %+v, want the data root and its free kibibytes read as bytes", room)
	}
	held := room.Repos["ocel-shop-web"]
	if held.Count != 3 || held.Largest != 300 {
		t.Fatalf("readHeadroom() held %+v, want three images whose largest is 300", held)
	}
	if want := int64(300); held.Needs() != want {
		t.Errorf("Needs() = %d, want %d: a full window leaves no unfilled slot and the incoming image is the one more", held.Needs(), want)
	}
}

func TestAnUnfilledWindowAsksForEverySlotItHasNotFilled(t *testing.T) {
	t.Parallel()

	room, err := readHeadroom("root=/var/lib/docker\nfree=1024\nrepo=ocel-shop-web\nsize=500\n")
	if err != nil {
		t.Fatalf("readHeadroom() = %v", err)
	}
	if want := int64(500 * 3); room.Needs() != want {
		t.Errorf("Needs() = %d, want %d: two slots are unfilled and the incoming image is a third", room.Needs(), want)
	}
}

func TestAFirstDeployIsProtectedByAConstantThatSaysItIsAGuess(t *testing.T) {
	t.Parallel()

	room, err := readHeadroom("root=/var/lib/docker\nfree=1024\nrepo=ocel-shop-web\n")
	if err != nil {
		t.Fatalf("readHeadroom() = %v", err)
	}
	if room.Needs() != FirstDeployFloor {
		t.Fatalf("Needs() = %d, want the floor %d: with nothing held there is no size to extrapolate", room.Needs(), FirstDeployFloor)
	}
	said := arithmetic(room)
	if !strings.Contains(said, "guessed constant") {
		t.Errorf("the refusal says %q, and a constant that will be wrong for someone has to be named as a guess", said)
	}
}

func TestATableThisHostCouldNotAnswerIsRefusedRatherThanReadAsRoom(t *testing.T) {
	t.Parallel()

	for what, said := range map[string]string{
		"a docker info that answered nothing": "",
		"a free column that is not a number":  "root=/var/lib/docker\nfree=plenty\n",
		"an image size that is not a number":  "root=/var/lib/docker\nfree=1024\nrepo=web\nsize=big\n",
	} {
		if room, err := readHeadroom(said); err == nil {
			t.Errorf("readHeadroom(%s) = %+v, want a refusal: a disk whose free space could not be read is not a disk with room", what, room)
		}
	}
}

func TestTheStateSelectorsSeparateExitedFromRestarting(t *testing.T) {
	t.Parallel()

	command := stateCommand(ProxyContainer)
	for _, wanted := range []string{".State.Status", ".State.ExitCode", ".State.Error", ".RestartCount", ".State.OOMKilled"} {
		if !strings.Contains(command, wanted) {
			t.Errorf("the proxy's state is read with %q, which names no %s: exited and restarting are different failures with different fixes", command, wanted)
		}
	}
	if got := stateField("Status=restarting ExitCode=1 RestartCount=9", "Status"); got != proxyRestarting {
		t.Errorf("stateField(Status) = %q, want %q", got, proxyRestarting)
	}
	if got := stateField("Status=exited ExitCode=1", "Status"); got != proxyExited {
		t.Errorf("stateField(Status) = %q, want %q", got, proxyExited)
	}
	if got := stateField("Error: No such object: ocel-proxy", "Status"); got != "" {
		t.Errorf("stateField over what docker says about a container it does not have = %q, want nothing", got)
	}
}

func TestTheHeadroomReadNamesTheAppsFilterAndNotTheWholeStore(t *testing.T) {
	t.Parallel()

	command := headroomCommand([]string{"ocel-shop-web"})
	if !strings.Contains(command, "reference='ocel-shop-web:*'") {
		t.Errorf("the headroom read is %q, and an unfiltered `docker image ls` sizes every image on a box ocel does not own", command)
	}
	if strings.Contains(command, "docker image ls --format") {
		t.Errorf("the headroom read is %q and lists images under no filter", command)
	}
}
