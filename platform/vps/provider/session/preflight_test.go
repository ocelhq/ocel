package session

import (
	"os/exec"
	"strings"
	"testing"
)

func answering() Facts {
	return Facts{Root: true, Systemd: true, Tools: bootstrapTools}
}

func TestALoginThatAnswersEveryRequirementIsLetPast(t *testing.T) {
	t.Parallel()

	if err := met(answering(), "ubuntu@203.0.113.10"); err != nil {
		t.Errorf("met() = %v, want a host that carries everything bootstrap asks for to be let past", err)
	}
}

func TestALoginThatMissesARequirementIsRefusedAndTold(t *testing.T) {
	t.Parallel()

	for name, drift := range map[string]func(*Facts){
		"neither root nor sudo": func(f *Facts) { f.Root, f.Sudo = false, false },
		"no systemd":            func(f *Facts) { f.Systemd = false },
		"no useradd":            func(f *Facts) { f.Tools = []string{"install"} },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			facts := answering()
			drift(&facts)
			err := met(facts, "ubuntu@203.0.113.10")
			if err == nil {
				t.Fatalf("met() let a host with %s past, and bootstrap would fail halfway through instead", name)
			}
			if !strings.Contains(err.Error(), "ubuntu@203.0.113.10") {
				t.Errorf("met() = %v, want the refusal to name the login it is about", err)
			}
		})
	}
}

func TestNoRequirementIsMetByAHostThatAnsweredNothing(t *testing.T) {
	t.Parallel()

	for _, need := range Requirements() {
		if need.Met(Facts{}) {
			t.Errorf("%q is met by a host that reported nothing, so the document names a requirement preflight does not check", need.Name)
		}
	}
}

func TestTheSurveyReportsTheToolsABootstrapWritesWith(t *testing.T) {
	t.Parallel()

	rendered, err := exec.Command("/bin/sh", "-c", survey).Output()
	if err != nil {
		t.Fatalf("run the survey: %v", err)
	}
	facts := readFacts(string(rendered))
	for _, tool := range []string{"install", "stat", "sha256sum"} {
		if !strings.Contains(strings.Join(facts.Tools, " "), tool) {
			t.Errorf("the survey read this machine's tools as %v, and %s is on it", facts.Tools, tool)
		}
	}
}

func TestTheSurveyReadsBackAsFacts(t *testing.T) {
	t.Parallel()

	facts := readFacts("uid=0\narch=aarch64\nkernel=6.8.0-31-generic\nos=Ubuntu 24.04.1 LTS\nsystemd=yes\nsudo=no\n")
	if !facts.Root || facts.Sudo || !facts.Systemd {
		t.Errorf("readFacts() = %+v, want a root login on a systemd host", facts)
	}
	if facts.OS != "Ubuntu 24.04.1 LTS" || facts.Arch != "aarch64" || facts.Kernel != "6.8.0-31-generic" {
		t.Errorf("readFacts() = %+v, want the host's own account of itself", facts)
	}
}

func TestAnUnprivilegedLoginReadsBackAsOne(t *testing.T) {
	t.Parallel()

	facts := readFacts("uid=1000\nsystemd=no\nsudo=no\n")
	if facts.Root || facts.Sudo || facts.Systemd {
		t.Errorf("readFacts() = %+v, want nothing claimed that the host did not report", facts)
	}
}
