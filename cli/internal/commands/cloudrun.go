package commands

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/ocelhq/ocel/cli/internal/clierror"
	"github.com/ocelhq/ocel/cli/internal/cloud"
	"github.com/ocelhq/ocel/cli/internal/console"
	"github.com/ocelhq/ocel/cli/internal/english"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/cli/internal/sourcearchive"
	"github.com/ocelhq/ocel/pkg/progress"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	consolev1 "github.com/ocelhq/ocel/pkg/proto/console/v1"
)

const (
	cloudVendor        = "Ocel Cloud"
	uploadProgressEach = time.Second
)

func (i Invocation) UploadToCloud(ctx context.Context, t Target, command string, tier environmentv1.Tier) (err error) {
	cfg := t.Project
	ctx, running, err := i.Events.Begin(ctx, command, cfg.Dir)
	if err != nil {
		return err
	}
	defer running.End(&err)

	check := running.Phase(progressv1.Phase_PHASE_CHECK)
	client := console.New(t.Cloud.ConsoleURL)
	session := t.Cloud.Credentials.AccessToken
	org, err := pickCloudOrganization(ctx, client, session, cfg.Dir, t.Cloud.ConsoleURL)
	if err != nil {
		check.End(err)
		return err
	}
	check.Identity(&streamv1.IdentityEvent{Project: cfg.Slug, Tier: tier, Origin: &streamv1.Party{
		Vendor:    cloudVendor,
		Account:   org.Name,
		Principal: t.Cloud.Credentials.Email,
		Location:  hostOf(t.Cloud.ConsoleURL),
	}})
	if t.Derived != "" {
		check.Say(t.Derived)
	}
	err = linkCloudProject(ctx, check, client, session, cfg, org, t.Cloud.ConsoleURL)
	check.End(err)
	if err != nil {
		return err
	}

	phase := running.Phase(progressv1.Phase_PHASE_BUILD)
	sourceID, err := uploadSource(ctx, phase, cfg, cloud.New(t.Cloud.Offer.APIURL, console.NewCloudTokens(client, session)))
	phase.End(err)
	if err != nil {
		return err
	}
	return &clierror.Error{
		Code:  clierror.CodeCloudUnsupported,
		Cause: fmt.Errorf("uploaded the source of %s to Ocel Cloud as %s, but this build of the CLI cannot yet start the job that runs `%s` there", cfg.Slug, sourceID, command),
	}
}

func uploadSource(ctx context.Context, phase *run.Span, cfg *project.Project, uploader *cloud.Client) (string, error) {
	reading := phase.Child(cfg.Slug, progress.Reading.Title("the source"))
	archive, err := sourcearchive.Pack(cfg.Dir)
	if err == nil && len(archive.Manifest.GetFiles()) == 0 {
		err = fmt.Errorf("%s holds no file to upload once what is ignored is left out", cfg.Dir)
	}
	if err != nil {
		reading.End(err)
		return "", err
	}
	var total uint64
	for _, file := range archive.Manifest.GetFiles() {
		total += file.GetSize()
	}
	reading.Say(fmt.Sprintf("%s (%s) under %s", english.Count(len(archive.Manifest.GetFiles()), "file"), english.Bytes(total), archive.Root))
	planned, err := uploader.Plan(ctx, archive)
	reading.End(err)
	if err != nil {
		return "", err
	}

	if len(planned.Missing) == 0 {
		phase.Say("Ocel Cloud already holds every file")
	} else {
		object := fmt.Sprintf("%s (%s) to Ocel Cloud", english.Count(len(planned.Missing), "file"), english.Bytes(planned.TotalBytes))
		uploading := phase.Child(cfg.Slug, progress.Title{Started: "Uploading " + object, Ended: "Uploaded " + object})
		err = uploader.Send(ctx, archive, planned, reportUpload(uploading))
		uploading.End(err)
		if err != nil {
			return "", err
		}
	}
	return uploader.Commit(ctx, archive.Manifest)
}

func reportUpload(span *run.Span) func(cloud.Progress) {
	var mu sync.Mutex
	var last time.Time
	return func(p cloud.Progress) {
		mu.Lock()
		defer mu.Unlock()
		if p.SentBytes == 0 || p.SentFiles == p.TotalFiles || time.Since(last) < uploadProgressEach {
			return
		}
		last = time.Now()
		span.Say(fmt.Sprintf("%s of %s · %d of %d files", english.Bytes(p.SentBytes), english.Bytes(p.TotalBytes), p.SentFiles, p.TotalFiles))
	}
}

func pickCloudOrganization(ctx context.Context, client *console.Client, session, projectDir, consoleURL string) (*console.Organization, error) {
	orgs, err := client.ListOrganizations(ctx, session)
	if err != nil {
		return nil, fmt.Errorf("list your organizations in %s: %w", consoleURL, err)
	}
	if len(orgs) == 0 {
		return nil, fmt.Errorf("you belong to no organization in %s yet — create one there first", consoleURL)
	}
	find := func(id string) *console.Organization {
		for i := range orgs {
			if orgs[i].ID == id {
				return &orgs[i]
			}
		}
		return nil
	}
	link, err := console.ReadLink(projectDir, consoleURL)
	if err != nil {
		return nil, err
	}
	if link != nil {
		if org := find(link.OrganizationID); org != nil {
			return org, nil
		}
		return nil, fmt.Errorf("this directory is linked to %s in an organization you no longer belong to — run `ocel link` to link it again", link.ProjectName)
	}
	if read, err := client.GetSession(ctx, session); err == nil && read != nil {
		if org := find(read.Session.ActiveOrganizationID); org != nil {
			return org, nil
		}
	}
	if len(orgs) == 1 {
		return &orgs[0], nil
	}
	slugs := make([]string, 0, len(orgs))
	for _, org := range orgs {
		slugs = append(slugs, org.Slug)
	}
	return nil, clierror.NewInputRequired(
		fmt.Errorf("you belong to %d organizations in %s and none is active — run `ocel link --org <slug>` to pick the one this project deploys under: %s", len(orgs), consoleURL, strings.Join(slugs, ", ")),
		"ocel link --org <slug>",
	)
}

func linkCloudProject(ctx context.Context, check *run.Span, client *console.Client, session string, cfg *project.Project, org *console.Organization, consoleURL string) error {
	if err := client.SetActiveOrganization(ctx, session, org.ID); err != nil {
		return fmt.Errorf("make %s your active organization in %s: %w", org.Name, consoleURL, err)
	}
	if link, err := console.ReadLink(cfg.Dir, consoleURL); err != nil || link != nil {
		return err
	}
	selected, err := findOrCreateProject(ctx, check, client, session, cfg.Slug, org)
	if err != nil {
		return err
	}
	if err := console.WriteLink(cfg.Dir, console.Link{
		APIURL:         consoleURL,
		OrganizationID: org.ID,
		ProjectID:      selected.GetId(),
		ProjectName:    selected.GetName(),
	}); err != nil {
		return err
	}
	check.Say(fmt.Sprintf("Linked this directory to %s (%s)", selected.GetSlug(), org.Name))
	return nil
}

func findOrCreateProject(ctx context.Context, check *run.Span, client *console.Client, session, slug string, org *console.Organization) (*consolev1.Project, error) {
	find := func() (*consolev1.Project, error) {
		projects, err := client.ListProjects(ctx, session)
		if err != nil {
			return nil, fmt.Errorf("list the projects in %s: %w", org.Name, err)
		}
		for _, p := range projects {
			if p.GetSlug() == slug {
				return p, nil
			}
		}
		return nil, nil
	}
	if found, err := find(); err != nil || found != nil {
		return found, err
	}
	creating := check.Child(slug, progress.Creating.Title("the project in "+org.Name))
	created, err := client.CreateProject(ctx, session, slug, slug)
	if console.IsConflict(err) {
		created, err = find()
		if err == nil && created == nil {
			err = errors.New("the console reports the project exists but does not list it")
		}
	}
	if err != nil {
		err = fmt.Errorf("create the project %s in %s: %w", slug, org.Name, err)
	}
	creating.End(err)
	return created, err
}

func hostOf(consoleURL string) string {
	if u, err := url.Parse(consoleURL); err == nil && u.Host != "" {
		return u.Host
	}
	return consoleURL
}
