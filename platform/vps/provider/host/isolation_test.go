package host

import (
	"strings"
	"testing"
)

func handedTo(spec Container) handoff {
	digest, err := envDigest(spec.Env)
	if err != nil {
		panic(err)
	}
	return handoff{path: EnvFile(spec.Class, spec.Name), digest: digest}
}

func running() map[string]string {
	return map[string]string{
		"the app container a deploy stands up": words(containerRun(valued(), handedTo(valued()))),
		"the proxy container a bootstrap runs": containerCommand(),
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

func TestNoContainerIsHandedTheKeyOrTheRecordsItSealed(t *testing.T) {
	t.Parallel()

	for what, command := range running() {
		for _, held := range []string{classRoot, stateRoot} {
			for _, mounting := range []string{quoted("--volume") + " " + quoted(held), "--volume " + held, "--mount", held + ":"} {
				if strings.Contains(command, mounting) {
					t.Errorf("%s runs %q and mounts %s: the seal key and every sealed record live under those paths, and neither is inside any container's mount namespace",
						what, command, held)
				}
			}
		}
	}
}

func TestTheEnvFileIsTheOnlyThingUnderTheStateRootAContainerIsToldAbout(t *testing.T) {
	t.Parallel()

	command := words(containerRun(valued(), handedTo(valued())))
	for _, named := range strings.Fields(command) {
		held := strings.Trim(named, "'")
		if !strings.HasPrefix(held, stateRoot) {
			continue
		}
		if held != EnvFile(valued().Class, valued().Name) {
			t.Errorf("standing a container up names %q, and the env file is the only path under %s a run has any business with", held, stateRoot)
		}
	}
}
