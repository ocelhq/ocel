package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/machine"
	"github.com/ocelhq/ocel/platform/vps/provider/shell"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

var trusted = provider.WebIdentityTrust{
	Issuer:         "https://console.example.com",
	Audience:       "ocel-target:target-1",
	OrganizationID: "org-1",
	TargetID:       "target-1",
}

func itemNamed(t *testing.T, items []machine.Item, name string) machine.Item {
	t.Helper()
	at := slices.IndexFunc(items, func(item machine.Item) bool { return item.Name == name })
	if at < 0 {
		t.Fatalf("no item writes %s", name)
	}
	return items[at]
}

func TestSshdTrustsTheBoxsAuthorityForTheDeployLoginAlone(t *testing.T) {
	t.Parallel()

	dropIn := string(itemNamed(t, ConsoleTrustItems(environment.TierProduction, trusted), SSHDDropIn).Content)
	lines := strings.Split(strings.TrimSpace(dropIn), "\n")
	if lines[0] != "Match User "+boxapi.DeployLogin {
		t.Errorf("the drop-in opens with %q, want it to apply to %s alone", lines[0], boxapi.DeployLogin)
	}
	if lines[len(lines)-1] != "Match all" {
		t.Errorf("the drop-in ends with %q, want the match closed so the lines sshd reads after it apply to every login", lines[len(lines)-1])
	}
	for _, want := range []string{"TrustedUserCAKeys " + boxapi.AuthorityPublicKey, "AuthorizedPrincipalsFile " + boxapi.AuthorizedPrincipals} {
		if !strings.Contains(dropIn, want) {
			t.Errorf("drop-in = %q, want %q", dropIn, want)
		}
	}
}

func TestTheBoxsConfigAndSshdNameTheSamePrincipal(t *testing.T) {
	t.Parallel()

	items := ConsoleTrustItems(environment.TierProduction, trusted)
	var config boxapi.AuthorityConfig
	if err := json.Unmarshal(itemNamed(t, items, boxapi.AuthorityConfigPath).Content, &config); err != nil {
		t.Fatal(err)
	}
	principals := strings.TrimSpace(string(itemNamed(t, items, boxapi.AuthorizedPrincipals).Content))
	if config.Principal != boxapi.DeployLogin || principals != config.Principal {
		t.Errorf("the config names %q and sshd lets in %q, want both %s", config.Principal, principals, boxapi.DeployLogin)
	}
}

func TestTheTierTrustsTheConsoleThePrepareNamed(t *testing.T) {
	t.Parallel()

	item := itemNamed(t, ConsoleTrustItems(environment.TierPreview, trusted), boxapi.ConsoleTrustPath(environment.TierPreview))
	var written boxapi.ConsoleTrust
	if err := json.Unmarshal(item.Content, &written); err != nil {
		t.Fatal(err)
	}
	want := boxapi.ConsoleTrust{Issuer: trusted.Issuer, Audience: trusted.Audience, OrganizationID: trusted.OrganizationID, TargetID: trusted.TargetID}
	if written != want {
		t.Errorf("trust = %+v, want %+v", written, want)
	}
	if item.Mode&0o004 == 0 {
		t.Errorf("trust mode = %04o, want it readable by the connector's login", item.Mode)
	}
}

func TestTheAuthorityKeyIsReadableByRootAloneAndItsPublicHalfBySshd(t *testing.T) {
	t.Parallel()

	key := itemNamed(t, AuthorityItems(), boxapi.AuthorityKey)
	if key.Mode != 0o600 || key.Owner != machine.RootOwner {
		t.Errorf("key = %04o %s, want 0600 and owned by root, so neither the connector nor a certificate's holder reads it", key.Mode, key.Owner)
	}
	minted := key.Command()
	for _, want := range []string{"ssh-keygen -q -t ed25519", "chown root:root " + shell.Quote(boxapi.AuthorityKey), "install -m 0644 -o root -g root /dev/stdin " + shell.Quote(boxapi.AuthorityPublicKey)} {
		if !strings.Contains(minted, want) {
			t.Errorf("the key's script = %q, want %q", minted, want)
		}
	}
	if strings.Contains(minted, boxapi.DeployLogin) {
		t.Errorf("the key's script = %q, and hands the key to %s", minted, boxapi.DeployLogin)
	}
	if !strings.Contains(minted, "if [ ! -f "+shell.Quote(boxapi.AuthorityKey)+" ]") {
		t.Errorf("the key's script = %q, want a key already minted kept", minted)
	}
}

func TestTheConnectorMaySignOnlyThroughTheHelper(t *testing.T) {
	t.Parallel()

	sudoers := string(itemNamed(t, AuthorityItems(), sudoersSign).Content)
	if want := boxapi.DeployLogin + " ALL=(root) NOPASSWD: " + LiveBinary + " " + boxapi.SignCommand + "\n"; sudoers != want {
		t.Errorf("sudoers = %q, want %q", sudoers, want)
	}
}

func TestAnAuthorityKeyAlreadyMintedIsReportedCurrent(t *testing.T) {
	t.Parallel()

	me, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	key := authorityKey()
	key.Name, key.Owner = filepath.Join(t.TempDir(), "user-ca"), me.Username
	if err := os.WriteFile(key.Name, []byte("a key"), 0o600); err != nil {
		t.Fatal(err)
	}
	rendered, err := exec.Command("/bin/sh", "-c", machine.SurveyScript([]machine.Item{key})).Output()
	if err != nil {
		t.Fatal(err)
	}
	observed, _, err := machine.ReadSurvey(string(rendered))
	if err != nil {
		t.Fatal(err)
	}
	if !(TrustReading{Observed: observed}).IsCurrent([]machine.Item{key}) {
		t.Errorf("survey = %q, want the key reported as current", rendered)
	}
}

func TestATrustReadingPlansOnlyWhatDiffers(t *testing.T) {
	t.Parallel()

	items := ConsoleTrustItems(environment.TierProduction, trusted)
	observed := machine.DigestsOf(items[:1])
	observed[items[1].ID()] = "drifted"
	changes := TrustReading{Observed: observed}.ListChanges(items)
	if changes[0].Action != provider.ActionKeep || changes[1].Action != provider.ActionUpdate || changes[2].Action != provider.ActionCreate {
		t.Errorf("changes = %+v, want keep, update, then create", changes)
	}
	if (TrustReading{Observed: observed}).IsCurrent(items) {
		t.Error("a reading with drift and absences is current")
	}
}

func sshdThat(t *testing.T, script string) (string, []string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "sshd"), []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	dropIn := filepath.Join(dir, "ocel.conf")
	if err := os.WriteFile(dropIn, sshdDropIn(), 0o644); err != nil {
		t.Fatal(err)
	}
	return dropIn, append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"))
}

func TestADropInSshdRefusesIsTakenOutSoTheNextRestartStillLetsLoginsIn(t *testing.T) {
	t.Parallel()

	dropIn, env := sshdThat(t, "echo '/etc/ssh/sshd_config.d/ocel.conf line 2: Bad configuration option: TrustedUserCAKeys' >&2; exit 255")
	cmd := exec.Command("/bin/sh", "-c", sshdValidation(dropIn))
	cmd.Env = env
	said, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("the check passed a config sshd refuses")
	}
	if !strings.Contains(string(said), "Bad configuration option") {
		t.Errorf("the check said %q, want sshd's own words", said)
	}
	if _, err := os.Stat(dropIn); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("stat %s = %v, want the drop-in removed", dropIn, err)
	}
}

func TestADropInSshdAcceptsStays(t *testing.T) {
	t.Parallel()

	dropIn, env := sshdThat(t, "exit 0")
	cmd := exec.Command("/bin/sh", "-c", sshdValidation(dropIn))
	cmd.Env = env
	if said, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("the check refused a config sshd accepts: %v %s", err, said)
	}
	if _, err := os.Stat(dropIn); err != nil {
		t.Errorf("stat %s = %v, want the drop-in kept", dropIn, err)
	}
}

func TestATrustAgreeingWithTheSiblingTierAndTheConnectorIsAccepted(t *testing.T) {
	t.Parallel()

	sibling := trusted
	if err := RefuseConflictingTrust(environment.TierPreview, trusted, TrustReading{Sibling: &sibling, ConnectorOrganization: trusted.OrganizationID}); err != nil {
		t.Fatal(err)
	}
	if err := RefuseConflictingTrust(environment.TierPreview, trusted, TrustReading{}); err != nil {
		t.Fatalf("a box with no other trust and no connector refused it: %v", err)
	}
}

func TestATrustTheConnectorWouldRefuseToServeIsRefusedNamingBoth(t *testing.T) {
	t.Parallel()

	other := trusted
	other.TargetID, other.Audience = "target-2", "ocel-target:target-2"
	err := RefuseConflictingTrust(environment.TierPreview, trusted, TrustReading{Sibling: &other})
	if err == nil || !strings.Contains(err.Error(), "target-2") || !strings.Contains(err.Error(), "target-1") || !strings.Contains(err.Error(), "--remove console-trust") {
		t.Errorf("err = %v, want both targets named and the removal that settles it", err)
	}
	err = RefuseConflictingTrust(environment.TierProduction, trusted, TrustReading{ConnectorOrganization: "org-2"})
	if err == nil || !strings.Contains(err.Error(), "org-2") || !strings.Contains(err.Error(), "org-1") {
		t.Errorf("err = %v, want both organizations named", err)
	}
}

func surveyLine(item machine.Item) string {
	return strings.Join([]string{item.Kind, item.Name, fmt.Sprintf("%o", item.Mode), item.Owner, item.Sum()}, "\t")
}

func trustedBoxAsTheDeployLogin(t *testing.T, recorded bool) *bench {
	t.Helper()
	items := ConsoleTrustItems(environment.TierProduction, trusted)
	digests, err := json.Marshal(machine.DigestsOf(items))
	if err != nil {
		t.Fatal(err)
	}
	box := newBench(nil)
	box.facts = ssh.Facts{Systemd: true, Arch: "x86_64"}
	box.floor = refusal.Refuse(refusal.CodeDenied, "ocel-deploy is neither root nor has passwordless sudo")
	box.answer = func(command string) (ssh.Result, bool) {
		switch {
		case strings.Contains(command, "for p in"):
			var lines []string
			for _, item := range items {
				if item.Name != sudoersSign {
					lines = append(lines, surveyLine(item))
				}
			}
			return ssh.Result{Stdout: strings.Join(lines, "\n") + "\n"}, true
		case strings.Contains(command, shell.Quote(boxapi.ConsoleTrustDigestsPath(environment.TierProduction))) && recorded:
			return ssh.Result{Stdout: string(digests)}, true
		case strings.Contains(command, shell.Quote(boxapi.ConsoleTrustPath(environment.TierProduction))):
			return ssh.Result{Stdout: string(consoleTrustContent(trusted))}, true
		case strings.HasPrefix(command, "if [ -f "):
			return ssh.Result{Stdout: trustAbsent + "\n"}, true
		}
		return ssh.Result{}, false
	}
	return box
}

func TestTheDeployLoginReadsTheTrustPartCurrentThoughItCannotSeeTheSudoersLine(t *testing.T) {
	t.Parallel()

	read, err := trustedBoxAsTheDeployLogin(t, true).host().ConsoleTrust().Read(context.Background(), environment.TierProduction)
	if err != nil {
		t.Fatal(err)
	}
	if !read.IsCurrent(ConsoleTrustItems(environment.TierProduction, trusted)) {
		t.Errorf("observed = %v, want the part current from what root recorded beside it", read.Observed)
	}
}

func TestATrustPartWithNothingRecordedBesideItReadsStaleToTheDeployLogin(t *testing.T) {
	t.Parallel()

	read, err := trustedBoxAsTheDeployLogin(t, false).host().ConsoleTrust().Read(context.Background(), environment.TierProduction)
	if err != nil {
		t.Fatal(err)
	}
	if read.IsCurrent(ConsoleTrustItems(environment.TierProduction, trusted)) {
		t.Error("the part reads current with its sudoers line unseen and nothing recorded for it")
	}
}
