package host

import (
	"strings"
	"testing"
)

func TestTheEngineInstallRunsNothingItHasNotHashedAndPinsTheVersionItInstalls(t *testing.T) {
	t.Parallel()

	command := engineCommand()
	if strings.Contains(command, "get.docker.com") {
		t.Errorf("the engine install fetches %q, which serves whatever docker publishes that day, so nothing pins what runs as root:\n%s", "get.docker.com", command)
	}
	if !strings.Contains(dockerSource, dockerScriptCommit) {
		t.Errorf("the install script is fetched from %s, which names no commit, so its bytes can change under the digest that pins them", dockerSource)
	}
	check := strings.Index(command, dockerScriptSum+` "$script" | sha256sum -c`)
	run := strings.Index(command, `sh "$script"`)
	if check < 0 || run < 0 || check > run {
		t.Fatalf("the engine install runs the script it fetched before checking it against %s:\n%s", dockerScriptSum, command)
	}
	if !strings.Contains(command[check:run], "exit 1") {
		t.Errorf("a script that does not hash as pinned is run anyway:\n%s", command)
	}
	if !strings.Contains(command, "VERSION="+dockerVersion+` sh "$script"`) {
		t.Errorf("the script is run with no VERSION, so it installs whatever docker's stable channel holds that day rather than %s:\n%s", dockerVersion, command)
	}
	if len(dockerScriptSum) != 64 || strings.Trim(dockerScriptSum, "0123456789abcdef") != "" {
		t.Errorf("%q is no sha256 hex digest", dockerScriptSum)
	}
	if len(dockerScriptCommit) != 40 || strings.Trim(dockerScriptCommit, "0123456789abcdef") != "" {
		t.Errorf("%q is no git commit", dockerScriptCommit)
	}
}
