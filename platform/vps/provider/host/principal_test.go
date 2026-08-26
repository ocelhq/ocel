package host

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

type account struct {
	shell    string
	home     string
	groups   string
	password string
}

func standing() account {
	return account{shell: "/bin/sh", home: "/var/lib/ocel", groups: "ocel-deploy docker", password: "*"}
}

func stubs(t *testing.T, held *account) string {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "log")
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	said := "printf '%s\\n' \"$(basename \"$0\") $*\" >>" + quoted(log) + "\n"

	passwd, shadow := "", ""
	identity := "exit 1\n"
	if held != nil {
		passwd = deployUser + ":x:997:997::" + held.home + ":" + held.shell
		shadow = deployUser + ":" + held.password + ":20000:0:99999:7:::"
		identity = "printf '%s\\n' " + quoted(held.groups) + "\n"
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

func TestAPrincipalThatAlreadyStandsIsMovedRatherThanRemade(t *testing.T) {
	t.Parallel()

	held := standing()
	dir := stubs(t, &held)
	sh(t, dir, principal().command())
	log := ran(t, dir)

	if strings.Contains(log, "useradd") {
		t.Errorf("writing a principal that already stands ran\n%s\nwant the account it found brought to what ocel writes", log)
	}
	if !strings.Contains(log, "usermod -g "+deployUser+" -G docker -d /var/lib/ocel -s /bin/sh "+deployUser) {
		t.Errorf("writing a principal that already stands ran\n%s\nwant every field ocel names set on it", log)
	}
}

func TestTheProbeAndTheWriteAgreeOnWhatAStandingPrincipalIs(t *testing.T) {
	t.Parallel()

	held := standing()
	observed, err := readSurvey(sh(t, stubs(t, &held), accountSurvey(deployUser)))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := observed[principal().ID()], principal().Digest(); got != want {
		t.Errorf("the probe read %q of a principal standing as ocel writes it, want %q", got, want)
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

			held := standing()
			drift(&held)
			observed, err := readSurvey(sh(t, stubs(t, &held), accountSurvey(deployUser)))
			if err != nil {
				t.Fatal(err)
			}
			if _, stood := observed[principal().ID()]; !stood {
				t.Fatal("the probe read no account where one stands, so a drifted principal reads as one nothing has created")
			}
			if observed[principal().ID()] == principal().Digest() {
				t.Errorf("the probe calls a principal with %s current, and nothing would ever bring it back", name)
			}
		})
	}
}

func TestOneSurveyReadsBackBothTheAccountAndThePaths(t *testing.T) {
	t.Parallel()

	held := standing()
	class := providerkit.ClassProduction
	items := Items(class, []byte(aKey+"\n"))
	observed, err := readSurvey(sh(t, stubs(t, &held), survey(items, StampPath(class))))
	if err != nil {
		t.Fatal(err)
	}
	if observed[principal().ID()] != principal().Digest() {
		t.Errorf("the survey read %q for the account, want the probe and the paths to answer in one round trip", observed[principal().ID()])
	}
}

func TestAPrincipalNothingCreatedIsNotStanding(t *testing.T) {
	t.Parallel()

	observed, err := readSurvey(sh(t, stubs(t, nil), accountSurvey(deployUser)))
	if err != nil {
		t.Fatal(err)
	}
	if _, stood := observed[principal().ID()]; stood {
		t.Error("the probe read an account on a host that has none")
	}
}
