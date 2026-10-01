package host

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/session"
)

func loggedInAs(user string, facts session.Facts) *bench {
	rig := machine(nil)
	rig.dest.User = user
	rig.facts = facts
	imaged(rig, false)
	return rig
}

func commandWith(t *testing.T, rig *bench, marker string) string {
	t.Helper()
	for _, command := range rig.commands() {
		if strings.Contains(command, marker) {
			return command
		}
	}
	t.Fatalf("nothing ran %q: %v", marker, rig.commands())
	return ""
}

func TestALoginWithSudoLoadsImagesAsTheDeployLogin(t *testing.T) {
	rig := loggedInAs("ubuntu", session.Facts{Sudo: true, Systemd: true})

	if _, err := rig.host().LoadImage(context.Background(), imageRef, strings.NewReader("tar-bytes")); err != nil {
		t.Fatalf("LoadImage() as a login with sudo = %v", err)
	}
	if loaded := commandWith(t, rig, "docker load"); !strings.HasPrefix(loaded, "sudo -n -u "+deployUser+" flock -x "+quoted(imagesLock)) {
		t.Errorf("the load ran as %q: %s is %s's and closed to every other login, so the lock is taken as %s", loaded, stateRoot, deployUser, deployUser)
	}
}

func TestARootLoginLoadsImagesAsTheDeployLogin(t *testing.T) {
	rig := loggedInAs("root", session.Facts{Root: true, Systemd: true})

	if _, err := rig.host().LoadImage(context.Background(), imageRef, strings.NewReader("tar-bytes")); err != nil {
		t.Fatalf("LoadImage() as root = %v", err)
	}
	if loaded := commandWith(t, rig, "docker load"); !strings.HasPrefix(loaded, "runuser -u "+deployUser+" -- flock -x "+quoted(imagesLock)) {
		t.Errorf("the load ran as %q: a lock root creates under %s is one %s cannot open on its next deploy", loaded, stateRoot, deployUser)
	}
}

func TestTheDeployLoginLoadsImagesAsItself(t *testing.T) {
	rig := loggedInAs(deployUser, session.Facts{Systemd: true})
	rig.floor = refusal.Refuse(refusal.CodeDenied, "%s can neither act as root nor run sudo without a password", deployUser)

	if _, err := rig.host().LoadImage(context.Background(), imageRef, strings.NewReader("tar-bytes")); err != nil {
		t.Fatalf("LoadImage() as %s = %v", deployUser, err)
	}
	if loaded := commandWith(t, rig, "docker load"); !strings.HasPrefix(loaded, "flock -x "+quoted(imagesLock)) {
		t.Errorf("the load ran as %q, want no elevation: %s owns %s and can sudo nothing", loaded, deployUser, stateRoot)
	}
}

func TestALoginWithSudoRecordsReleasesAsTheDeployLogin(t *testing.T) {
	rig := loggedInAs("ubuntu", session.Facts{Sudo: true, Systemd: true})

	if err := rig.host().Promote(context.Background(), environment.TierProduction, "shop", "web", imageRef); err != nil {
		t.Fatalf("Promote() as a login with sudo = %v", err)
	}
	if recorded := commandWith(t, rig, quoted(releasesHelper)); !strings.HasPrefix(recorded, "sudo -n -u "+deployUser+" "+quoted(releasesHelper)) {
		t.Errorf("the release was recorded as %q, want it written as %s, who owns %s", recorded, deployUser, releasesRoot)
	}
}

func TestALoginWithoutSudoIsToldToDeployAsTheDeployLogin(t *testing.T) {
	rig := loggedInAs("ubuntu", session.Facts{Systemd: true})
	rig.floor = refusal.Refuse(refusal.CodeDenied, "ubuntu@ocelbox can neither act as root nor run sudo without a password")

	_, err := rig.host().LoadImage(context.Background(), imageRef, strings.NewReader("tar-bytes"))
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeDenied {
		t.Fatalf("LoadImage() as a login that cannot sudo = %v, want a denial", err)
	}
	if !strings.Contains(err.Error(), deployUser) || !strings.Contains(err.Error(), stateRoot) {
		t.Errorf("the denial reads %q, and never names %s or the login that can reach it", err, stateRoot)
	}
	for _, command := range rig.commands() {
		if strings.Contains(command, "docker load") {
			t.Errorf("the load ran as %q where it can only be refused", command)
		}
	}
}
