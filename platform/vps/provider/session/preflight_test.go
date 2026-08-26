package session

import "testing"

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
