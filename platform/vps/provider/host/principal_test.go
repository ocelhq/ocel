package host

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type account struct {
	shell    string
	home     string
	groups   string
	password string
}

func decidedAccount() account {
	return account{shell: "/bin/sh", home: "/var/lib/ocel", groups: "ocel-deploy docker", password: "*"}
}

func stubs(t *testing.T, existing *account) string {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "log")
	write := func(name, body string) {
		t.Helper()
		executable(t, filepath.Join(dir, name), "#!/bin/sh\n"+body)
	}
	said := "printf '%s\\n' \"$(basename \"$0\") $*\" >>" + quoted(log) + "\n"

	passwd, shadow := "", ""
	identity := "exit 1\n"
	if existing != nil {
		passwd = deployUser + ":x:997:997::" + existing.home + ":" + existing.shell
		identity = "printf '%s\\n' " + quoted(existing.groups) + "\n"
		if existing.password != "" {
			shadow = deployUser + ":" + existing.password + ":20000:0:99999:7:::"
		}
	}
	write("getent", said+`case "$1 $2" in
'passwd `+deployUser+`') [ -n `+quoted(passwd)+` ] && printf '%s\n' `+quoted(passwd)+` || exit 2 ;;
'shadow `+deployUser+`') [ -n `+quoted(shadow)+` ] && printf '%s\n' `+quoted(shadow)+` || exit 2 ;;
*) exit 2 ;;
esac`)
	write("id", identity)
	for _, name := range []string{"useradd", "usermod", "groupadd"} {
		write(name, said)
	}
	write("passwd", said+"exit 0")
	return dir
}

func ran(t *testing.T, dir string) string {
	t.Helper()
	rendered, err := os.ReadFile(filepath.Join(dir, "log"))
	if errors.Is(err, os.ErrNotExist) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(rendered)
}

func sh(t *testing.T, dir, script string) string {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", script)
	cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"))
	var stderr strings.Builder
	cmd.Stderr = &stderr
	rendered, err := cmd.Output()
	if err != nil {
		t.Fatalf("run %q: %v\n%s", script, err, stderr.String())
	}
	return string(rendered)
}

func TestThePrincipalIsWrittenWithALockedPasswordAndNeverThroughPasswd(t *testing.T) {
	t.Parallel()

	dir := stubs(t, nil)
	sh(t, dir, principal().command())
	log := ran(t, dir)

	for _, want := range []string{
		"groupadd -r docker",
		"useradd -r -M -g " + deployUser + " -G docker -d /var/lib/ocel -s /bin/sh " + deployUser,
		"usermod -p * " + deployUser,
	} {
		if !strings.Contains(log, want) {
			t.Errorf("writing the principal ran\n%s\nwant it to run %q", log, want)
		}
	}
	if strings.Contains("\n"+log, "\npasswd ") {
		t.Errorf("writing the principal ran\n%s\nand passwd -l locks the account out of sshd, keys and all", log)
	}
}

func TestAPrincipalThatAlreadyExistsIsMovedRatherThanRemade(t *testing.T) {
	t.Parallel()

	existing := decidedAccount()
	dir := stubs(t, &existing)
	sh(t, dir, principal().command())
	log := ran(t, dir)

	if strings.Contains(log, "useradd") {
		t.Errorf("writing a principal that already exists ran\n%s\nwant the account it found brought to what ocel writes", log)
	}
	if !strings.Contains(log, "usermod -g "+deployUser+" -aG docker -d /var/lib/ocel -s /bin/sh "+deployUser) {
		t.Errorf("writing a principal that already exists ran\n%s\nwant every field ocel names set on it, and the supplementary list appended to rather than replaced", log)
	}
}

func TestTheProbeAndTheWriteAgreeOnWhatACurrentPrincipalIs(t *testing.T) {
	t.Parallel()

	existing := decidedAccount()
	observed, _, err := readSurvey(sh(t, stubs(t, &existing), deployLogin().survey()))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := observed[principal().ID()], principal().Digest(); got != want {
		t.Errorf("the probe read %q of a principal that exists as ocel writes it, want %q", got, want)
	}
}

func TestAPrincipalThatDriftedFromWhatOcelWroteIsNotCurrent(t *testing.T) {
	t.Parallel()

	for name, drift := range map[string]func(*account){
		"a shell that refuses a login": func(a *account) { a.shell = "/usr/sbin/nologin" },
		"a home somewhere else":        func(a *account) { a.home = "/home/ocel-deploy" },
		"no docker group":              func(a *account) { a.groups = deployUser },
		"a password that locks out":    func(a *account) { a.password = "!" },
		"a password of its own":        func(a *account) { a.password = "$6$salt$hash" },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			existing := decidedAccount()
			drift(&existing)
			observed, _, err := readSurvey(sh(t, stubs(t, &existing), deployLogin().survey()))
			if err != nil {
				t.Fatal(err)
			}
			if _, present := observed[principal().ID()]; !present {
				t.Fatal("the probe read no account where one exists, so a drifted principal reads as one nothing has created")
			}
			if observed[principal().ID()] == principal().Digest() {
				t.Errorf("the probe calls a principal with %s current, and nothing would ever bring it back", name)
			}
		})
	}
}

func TestAGetentThatWillNotRunIsNotAHostWithNoAccount(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	executable(t, filepath.Join(dir, "getent"), "#!/bin/sh\nexit 126\n")
	executable(t, filepath.Join(dir, "id"), "#!/bin/sh\nexit 1\n")

	observed, _, err := readSurvey(sh(t, dir, deployLogin().survey()))
	if err == nil {
		t.Fatalf("a survey whose getent could not be run read %v, and an account read as absent is one apply writes over: the deploy principal is recreated and its password reset under whatever is already logging in as it", observed)
	}
	if !strings.Contains(err.Error(), deployUser) {
		t.Errorf("the refusal reads %q and never names the account nothing could be read about", err)
	}
}

func TestAGetentThatFindsNoSuchAccountIsAnAbsenceAndNotARefusal(t *testing.T) {
	t.Parallel()

	observed, _, err := readSurvey(sh(t, stubs(t, nil), deployLogin().survey()))
	if err != nil {
		t.Fatalf("a survey of a host that has no such account = %v, want the absence a first bootstrap reads", err)
	}
	if _, present := observed[principal().ID()]; present {
		t.Errorf("the probe read %s on a host whose getent knows no such account", principal().ID())
	}
}

func TestOneSurveyReadsBackBothTheAccountAndThePaths(t *testing.T) {
	t.Parallel()

	existing := decidedAccount()
	class := edge.ClassProduction
	items := Items(class, []byte(aKey+"\n"), ArchAMD64, Front{})
	observed, _, err := readSurvey(sh(t, stubs(t, &existing), survey(items, StampPath(class))))
	if err != nil {
		t.Fatal(err)
	}
	if observed[principal().ID()] != principal().Digest() {
		t.Errorf("the survey read %q for the account, want the probe and the paths to answer in one round trip", observed[principal().ID()])
	}
}

func TestAPrincipalNothingCreatedIsNotPresent(t *testing.T) {
	t.Parallel()

	observed, _, err := readSurvey(sh(t, stubs(t, nil), deployLogin().survey()))
	if err != nil {
		t.Fatal(err)
	}
	if _, present := observed[principal().ID()]; present {
		t.Error("the probe read an account on a host that has none")
	}
}

func described(t *testing.T) map[string]string {
	t.Helper()
	facts := map[string]string{}
	for _, line := range strings.Split(strings.TrimRight(string(principal().Content), "\n"), "\n") {
		key, value, split := strings.Cut(line, "=")
		if !split {
			t.Fatalf("the account description contains %q, which names no fact", line)
		}
		facts[key] = value
	}
	return facts
}

func TestEveryFactTheAccountDescriptionContainsIsOneTheWriteSets(t *testing.T) {
	t.Parallel()

	facts := described(t)
	written := deployLogin().command()
	for fact, flag := range map[string]string{"shell": "-s ", "home": "-d "} {
		if !strings.Contains(written, flag+quoted(facts[fact])) {
			t.Errorf("the account description reads %s=%s and the write never sets it:\n%s", fact, facts[fact], written)
		}
	}
	if locked := facts["password"] == lockedFact; locked != strings.Contains(written, "usermod -p "+quoted(lockedPassword)) {
		t.Errorf("the account description reads password=%s and the write locks it %v:\n%s", facts["password"], !locked, written)
	}
}

func TestTheGroupTheAccountDescriptionNamesIsTheGroupBothBranchesAdd(t *testing.T) {
	t.Parallel()

	group := described(t)["group"]
	written := deployLogin().command()
	branches := 0
	for _, line := range strings.Split(written, "\n") {
		if !strings.HasPrefix(line, "useradd ") && !strings.HasPrefix(line, "usermod -g ") {
			continue
		}
		branches++
		if adds := group != "" && strings.Contains(line, "G "+quoted(group)); adds != (group != "") {
			t.Errorf("the account description reads group=%s and %q adds it %v, so the document would claim a membership nothing writes", group, line, adds)
		}
	}
	if branches != 2 {
		t.Errorf("the write has %d branches that create or move the account, want the group added on both:\n%s", branches, written)
	}
}

func TestAPasswordFieldTheHostWillNotShowSurveysTheLoginNotAtAll(t *testing.T) {
	t.Parallel()

	probed := func(password string) (string, bool) {
		t.Helper()
		existing := decidedAccount()
		existing.password = password
		observed, _, err := readSurvey(sh(t, stubs(t, &existing), deployLogin().survey()))
		if err != nil {
			t.Fatal(err)
		}
		digest, present := observed[principal().ID()]
		return digest, present
	}

	if _, present := probed(""); present {
		t.Error("a shadow field the host will not show surveys as a login, and every reading a login without root takes would call the principal drifted forever")
	}
	unlocked, present := probed("$6$salt$hash")
	if !present {
		t.Fatal("the probe read no account where one exists with a password on it")
	}
	if unlocked == principal().Digest() {
		t.Error("a login with a password reads as the locked one ocel wrote, and drift would never be seen")
	}
}
