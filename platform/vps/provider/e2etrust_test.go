package vps_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const trustQuestion = "Trust that key and record"

var owedABootstrap = regexp.MustCompile("⚠ not bootstrapped\n\\s+→ run `ocel bootstrap production`")

func TestE2EFirstContactIsTheUsersDecisionAndTheRunGoesOnThroughIt(t *testing.T) {
	run := e2e(t)
	run.forgets(t)

	rendered, err := run.onATerminal(t, []string{"doctor"}, trustQuestion, "y")
	if err != nil {
		t.Fatalf("`ocel doctor` after the fingerprint was accepted = %v\n%s", err, rendered)
	}
	if !strings.Contains(rendered, "SHA256:") {
		t.Errorf("first contact never showed a fingerprint to decide on:\n%s", rendered)
	}
	if !owedABootstrap.MatchString(rendered) {
		t.Errorf("the accepted key did not re-drive the command it refused:\n%s", rendered)
	}

	recorded, err := os.ReadFile(run.store)
	if err != nil {
		t.Fatalf("nothing was written to %s, so ocel's trust and ssh's trust are not the same trust: %v", run.store, err)
	}
	if !strings.Contains(string(recorded), run.vm.addr) {
		t.Errorf("%s holds no entry for %s:\n%s", run.store, run.vm.addr, recorded)
	}
	if !strings.Contains(rendered, run.store) {
		t.Errorf("the question never named the file it would write to:\n%s", rendered)
	}
}

func TestE2EAChangedHostKeyIsRefusedOutrightAndNothingIsAsked(t *testing.T) {
	run := e2e(t)
	decoy := run.decoys(t)
	write(t, run.store, decoy)

	rendered, err := run.onATerminal(t, []string{"doctor"}, "", "")
	if err == nil {
		t.Fatalf("`ocel doctor` against a changed host key succeeded, and a possible man in the middle passed unremarked:\n%s", rendered)
	}
	for _, want := range []string{"changed", "got", "want", "ssh-keygen -R"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("the refusal never says %q, and the user is left without got-vs-want or the remedy:\n%s", want, rendered)
		}
	}
	if strings.Contains(rendered, trustQuestion) {
		t.Errorf("a changed host key was offered as a question rather than refused:\n%s", rendered)
	}
	held, err := os.ReadFile(run.store)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(held, []byte(decoy)) {
		t.Errorf("the refusal edited %s, and a mismatch must never touch the user's trust store", run.store)
	}
}

func (j journey) decoys(t *testing.T) string {
	t.Helper()
	key := filepath.Join(t.TempDir(), "decoy")
	made := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-f", key, "-N", "", "-C", "ocel-e2e-decoy")
	if rendered, err := made.CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen: %v\n%s", err, rendered)
	}
	public, err := os.ReadFile(key + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(string(public))
	if len(fields) < 2 {
		t.Fatalf("ssh-keygen wrote a public key nothing can key a known_hosts line on: %q", public)
	}
	return j.vm.addr + " " + fields[0] + " " + fields[1] + "\n"
}
