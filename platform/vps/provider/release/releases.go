package release

import (
	"context"
	_ "embed"
	"fmt"
	"strings"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/shell"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

//go:embed releases.sh
var HelperScript []byte

type Images struct{ box *ssh.Box }

func NewImages(box *ssh.Box) *Images { return &Images{box: box} }

func Repository(imageRef string) (string, bool) {
	if strings.Contains(imageRef, "@") {
		return "", false
	}
	at := strings.LastIndex(imageRef, ":")
	if at <= 0 || strings.Contains(imageRef[at+1:], "/") || at+1 == len(imageRef) {
		return "", false
	}
	return imageRef[:at], true
}

func Scope(project, app string) string { return naming.Sanitize(project) + "/" + app }

func (i *Images) Promote(ctx context.Context, tier environment.Tier, project, app, imageRef string) error {
	_, err := i.releases(ctx, "record "+imageRef+" as "+app+"'s release",
		Scope(project, app), "promote", string(tier), imageRef)
	return err
}

func (i *Images) Forget(ctx context.Context, tier environment.Tier, project, app string) error {
	_, err := i.releases(ctx, "forget "+app+"'s releases",
		Scope(project, app), "forget", string(tier))
	return err
}

type Swept struct {
	Removed []string
	Unused  []string
}

func (i *Images) Reconcile(ctx context.Context, project, app, imageRef string) (Swept, error) {
	repository, named := Repository(imageRef)
	if !named {
		return Swept{}, refusal.Refuse(refusal.CodeInvalid,
			"%s runs %s, which names no repository and tag", app, imageRef)
	}
	said, err := i.releases(ctx, "reconcile "+app+"'s images", Scope(project, app), "reconcile", repository)
	if err != nil {
		return Swept{}, err
	}
	var swept Swept
	for line := range strings.Lines(said) {
		kind, image, _ := strings.Cut(strings.TrimSpace(line), " ")
		switch {
		case image == "":
		case kind == "removed":
			swept.Removed = append(swept.Removed, image)
		case kind == "unused":
			swept.Unused = append(swept.Unused, image)
		}
	}
	return swept, nil
}

func (i *Images) Settle(ctx context.Context, project, app string, imageRefs []string) error {
	if len(imageRefs) == 0 {
		return nil
	}
	_, err := i.releases(ctx, "settle "+app+"'s removed images", Scope(project, app), append([]string{"settle"}, imageRefs...)...)
	return err
}

func (i *Images) IsClaimed(ctx context.Context, project, app, imageRef string) (bool, error) {
	said, err := i.releases(ctx, "read whether a release of "+app+" still names "+imageRef, Scope(project, app), "claimed", imageRef)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(said) == imageRef, nil
}

func (i *Images) archive(ctx context.Context, project, app, imageRef string) error {
	_, err := i.releases(ctx, "archive "+imageRef+" for a rollback to load again", Scope(project, app), "archive", imageRef)
	return err
}

func (i *Images) Ensure(ctx context.Context, project, app, imageRef string) (bool, error) {
	said, err := i.box.Run(ctx, ssh.Deploy, "load "+imageRef+" from its archive if the engine lacks it", "flock -x "+shell.Quote(boxapi.ImagesLock)+" "+releasesCommand(Scope(project, app), "restore", imageRef), nil)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(said) != "", nil
}

func (i *Images) CheckReleases(ctx context.Context, tier environment.Tier) provider.HostCheck {
	check := provider.HostCheck{Subject: boxapi.ReleasesDir, Verdict: provider.HostFail}
	acting, err := ssh.Deploy, i.box.Assume(ctx, ssh.Deploy)
	if err != nil {
		check.Finding = fmt.Sprintf("read which releases %s keeps for `ocel rollback`: %v", i.box.Named(), err)
		return check
	}
	said, err := i.box.Run(ctx, acting, "read which releases kept for `ocel rollback` lost their image", LostCommand(boxapi.ReleasesDir, boxapi.ReleasesHelper, tier), nil)
	if err != nil {
		check.Finding = err.Error()
		return check
	}
	var lost []string
	for line := range strings.Lines(said) {
		if scope, imageRef, found := strings.Cut(strings.TrimSpace(line), " "); found {
			lost = append(lost, fmt.Sprintf("%s keeps %s", scope, imageRef))
		}
	}
	if len(lost) == 0 {
		check.Verdict = provider.HostPass
		check.Finding = fmt.Sprintf("every release kept for `ocel rollback` in %s has its container or an archive of its image", tier)
		return check
	}
	check.Verdict = provider.HostNeedsAction
	check.Finding = fmt.Sprintf("releases kept for `ocel rollback` in %s have neither a container nor an archive of their image, so a rollback cannot start them: %s",
		tier, strings.Join(lost, "; "))
	check.Fix = "deploy such a release again if you need to return to it; the next deploys drop it from the releases a rollback can reach"
	return check
}

func LostCommand(root, helper string, tier environment.Tier) string {
	return "status=0\n" +
		"for dir in " + shell.Quote(root) + "/*/*/; do\n" +
		"[ -d \"$dir\" ] || continue\n" +
		"scope=${dir%/}\n" +
		"scope=${scope#" + shell.Quote(root+"/") + "}\n" +
		"said=$(" + shell.Quote(helper) + " \"$scope\" lost " + shell.Quote(string(tier)) + ") || status=1\n" +
		"printf '%s\\n' \"$said\" | sed -n \"s|^lost |$scope |p\"\n" +
		"done\n" +
		"exit \"$status\"\n"
}

func (i *Images) releases(ctx context.Context, what, scope string, args ...string) (string, error) {
	return i.box.Run(ctx, ssh.Deploy, what, releasesCommand(scope, args...), nil)
}

func releasesCommand(scope string, args ...string) string {
	command := shell.Quote(boxapi.ReleasesHelper) + " " + shell.Quote(scope)
	for _, arg := range args {
		command += " " + shell.Quote(arg)
	}
	return command
}
