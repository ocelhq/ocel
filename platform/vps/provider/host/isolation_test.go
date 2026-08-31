package host

import (
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

func handedTo(spec Container) handoff {
	held, err := handing(spec)
	if err != nil {
		panic(err)
	}
	return held
}

const (
	appContainer   = "the app container a deploy stands up"
	proxyContainer = "the proxy container a bootstrap runs"
)

func running() map[string]string {
	return map[string]string{
		appContainer:   words(containerRun(valued(), handedTo(valued()))),
		proxyContainer: words(proxyRun()),
	}
}

func owed() map[string][]string {
	return map[string][]string{
		appContainer:   {EnvFile(valued().Class, valued().Name)},
		proxyContainer: {proxyRoot, ProxyPins, ProxyData},
	}
}

func TestNoContainerIsHandedTheSocketThatIsRootUnderAnotherName(t *testing.T) {
	t.Parallel()

	for what, command := range running() {
		if strings.Contains(command, "docker.sock") {
			t.Errorf("%s runs %q: the daemon socket is root on this machine, and a container holding it opens every sealed value and reads every sibling's environment",
				what, command)
		}
	}
}

func TestNoContainerSharesAProcessNamespaceWithAnother(t *testing.T) {
	t.Parallel()

	for what, command := range running() {
		for _, shared := range []string{"--pid=host", "--pid=container:", quoted("--pid")} {
			if strings.Contains(command, shared) {
				t.Errorf("%s runs %q, and %q makes a sibling's /proc/1/environ readable from inside it",
					what, command, shared)
			}
		}
	}
}

func TestNoContainerIsRunPrivileged(t *testing.T) {
	t.Parallel()

	for what, command := range running() {
		if strings.Contains(command, "--privileged") {
			t.Errorf("%s runs %q, and a privileged container is the host", what, command)
		}
	}
}

func source(token string) string {
	held := strings.Trim(token, "'")
	if from, _, cut := strings.Cut(held, ":"); cut {
		return from
	}
	return held
}

func underARoot(path string) bool {
	for _, root := range []string{classRoot, stateRoot} {
		if path == root || strings.HasPrefix(path, root+"/") {
			return true
		}
	}
	return false
}

func holds(held []string, path string) bool {
	for _, one := range held {
		if one == path {
			return true
		}
	}
	return false
}

func TestEveryPathAContainerIsToldAboutUnderTheKeyOrTheRecordsIsOneItIsOwed(t *testing.T) {
	t.Parallel()

	allowed := owed()
	for what, command := range running() {
		if len(allowed[what]) == 0 {
			t.Fatalf("%s is rendered by this bench and nothing says which paths under %s and %s it is owed, so what it names proves nothing",
				what, classRoot, stateRoot)
		}
		named := 0
		for _, token := range strings.Fields(command) {
			path := source(token)
			if !underARoot(path) {
				continue
			}
			named++
			if !holds(allowed[what], path) {
				t.Errorf("%s runs %q and names %q, which is none of %v: the seal key, every sealed record and every other app's values live under %s and %s, and a run has business with nothing there but what it is owed",
					what, command, path, allowed[what], classRoot, stateRoot)
			}
		}
		if named == 0 {
			t.Errorf("%s names no path under %s or %s at all, so this walk reads no token of it and would say the same of a run that mounted every one of them",
				what, classRoot, stateRoot)
		}
	}
}

func TestNothingAContainerIsOwedIsTheKeyTheRecordsOrTheClassStateItself(t *testing.T) {
	t.Parallel()

	for _, class := range []providerkit.Class{providerkit.ClassProduction, providerkit.ClassPreview} {
		for what, allowed := range owed() {
			for _, path := range allowed {
				for _, refused := range []string{ClassDir(class), RecordsDir(class), SealKeyPath(class)} {
					if path == refused || strings.HasPrefix(path, refused+"/") {
						t.Errorf("%s is owed %q, which stands under %s: the key that opens every sealed value and the records it sealed are what that path holds",
							what, path, refused)
					}
				}
			}
		}
	}
}

func TestEveryContainerThisPackageRunsIsHeldToTheIsolationRules(t *testing.T) {
	t.Parallel()

	rendered(t, `[]string{"docker", "run"`, []string{"containerRun", "proxyRun"},
		"a container run built somewhere this bench does not read is held to none of the rules in this file")
}
