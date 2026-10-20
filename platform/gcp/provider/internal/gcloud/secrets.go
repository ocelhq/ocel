package gcloud

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"google.golang.org/api/googleapi"
	"google.golang.org/api/secretmanager/v1"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
)

const (
	FirstSecretVersion = "1"
	latestVersion      = "latest"
	enabledVersions    = "state:ENABLED"
	failedPrecondition = "FAILED_PRECONDITION"
)

func (c *Clients) EnsureSecret(ctx context.Context, service *secretmanager.Service, tier environment.Tier, name string) error {
	_, err := googleretry.Attempted(ctx, service.Projects.Secrets.Create("projects/"+c.Project(), &secretmanager.Secret{
		Replication: &secretmanager.Replication{Automatic: &secretmanager.Automatic{}},
		Tags:        c.InlineTierTags(tier),
	}).SecretId(name).Context(ctx).Do)
	if err == nil {
		return nil
	}
	if !Taken(err) {
		return fmt.Errorf("create the %s secret: %w", name, err)
	}
	if !c.TierTagged() {
		return nil
	}
	tagged, err := c.SecretTagged(ctx, name)
	if err != nil {
		return err
	}
	return c.EnsureTierTag(ctx, tier, tagged)
}

func (c *Clients) ReadSecretVersion(ctx context.Context, service *secretmanager.Service, name, version string) ([]byte, bool, error) {
	read, err := googleretry.Attempted(ctx, service.Projects.Secrets.Versions.Access(SecretPath(c.Project(), name)+"/versions/"+version).Context(ctx).Do)
	switch {
	case Absent(err):
		return nil, false, nil
	case unreadableVersion(err):
		return nil, true, nil
	case err != nil:
		return nil, false, fmt.Errorf("read version %s of the %s secret: %w", version, name, err)
	case read.Payload == nil:
		return nil, true, nil
	}
	payload, err := base64.StdEncoding.DecodeString(read.Payload.Data)
	if err != nil {
		return nil, false, fmt.Errorf("read version %s of the %s secret: %w", version, name, err)
	}
	return payload, true, nil
}

func unreadableVersion(err error) bool {
	var answered *googleapi.Error
	if !errors.As(err, &answered) || answered.Code != http.StatusBadRequest {
		return false
	}
	var said struct {
		Error struct {
			Status string `json:"status"`
		} `json:"error"`
	}
	return json.Unmarshal([]byte(answered.Body), &said) == nil && said.Error.Status == failedPrecondition
}

func (c *Clients) AddSecretVersion(ctx context.Context, service *secretmanager.Service, name string, payload []byte) (*secretmanager.SecretVersion, error) {
	added, err := googleretry.Attempted(ctx, service.Projects.Secrets.AddVersion(SecretPath(c.Project(), name), &secretmanager.AddSecretVersionRequest{
		Payload: &secretmanager.SecretPayload{Data: base64.StdEncoding.EncodeToString(payload)},
	}).Context(ctx).Do)
	if err != nil {
		return nil, fmt.Errorf("write a version of the %s secret: %w", name, err)
	}
	return added, nil
}

func (c *Clients) EnsureLatestVersion(ctx context.Context, service *secretmanager.Service, tier environment.Tier, name string, payload []byte) (string, error) {
	read, err := googleretry.Attempted(ctx, service.Projects.Secrets.Versions.Access(SecretPath(c.Project(), name)+"/versions/"+latestVersion).Context(ctx).Do)
	switch {
	case Absent(err):
		if err := c.EnsureSecret(ctx, service, tier, name); err != nil {
			return "", err
		}
	case unreadableVersion(err):
	case err != nil:
		return "", fmt.Errorf("read the latest version of the %s secret: %w", name, err)
	case read.Payload != nil && read.Payload.Data == base64.StdEncoding.EncodeToString(payload):
		number, err := versionNumber(read.Name)
		if err != nil {
			return "", fmt.Errorf("read the latest version of the %s secret: %w", name, err)
		}
		return strconv.Itoa(number), nil
	}
	added, err := c.AddSecretVersion(ctx, service, name, payload)
	if err != nil {
		return "", err
	}
	number, err := versionNumber(added.Name)
	if err != nil {
		return "", fmt.Errorf("read the version of the %s secret just written: %w", name, err)
	}
	return strconv.Itoa(number), nil
}

func (c *Clients) DestroyUnpinnedVersions(ctx context.Context, service *secretmanager.Service, name string, pinned []string) error {
	newest := 0
	kept := map[int]bool{}
	for _, version := range pinned {
		number, err := strconv.Atoi(version)
		if err != nil {
			return fmt.Errorf("read version %q of the %s secret a revision pins: %w", version, name, err)
		}
		kept[number] = true
		newest = max(newest, number)
	}
	older, err := c.enabledVersionsBefore(ctx, service, name, newest)
	if err != nil {
		return err
	}
	for _, version := range older {
		if kept[version.number] {
			continue
		}
		if err := c.destroyVersion(ctx, service, name, version.name); err != nil {
			return err
		}
	}
	return nil
}

func (c *Clients) DestroyVersionsBefore(ctx context.Context, service *secretmanager.Service, name string, kept *secretmanager.SecretVersion) error {
	return c.destroyVersionsSparing(ctx, service, name, kept, 0)
}

func (c *Clients) DestroyVersionsBeforePrevious(ctx context.Context, service *secretmanager.Service, name string, kept *secretmanager.SecretVersion) error {
	return c.destroyVersionsSparing(ctx, service, name, kept, 1)
}

func (c *Clients) destroyVersionsSparing(ctx context.Context, service *secretmanager.Service, name string, kept *secretmanager.SecretVersion, spared int) error {
	newest, err := versionNumber(kept.Name)
	if err != nil {
		return fmt.Errorf("read the version of the %s secret just written: %w", name, err)
	}
	older, err := c.enabledVersionsBefore(ctx, service, name, newest)
	if err != nil {
		return err
	}
	for _, version := range older[min(spared, len(older)):] {
		if err := c.destroyVersion(ctx, service, name, version.name); err != nil {
			return err
		}
	}
	return nil
}

type numberedVersion struct {
	name   string
	number int
}

func (c *Clients) enabledVersionsBefore(ctx context.Context, service *secretmanager.Service, name string, newest int) ([]numberedVersion, error) {
	var older []numberedVersion
	for token := ""; ; {
		page, err := googleretry.Attempted(ctx, service.Projects.Secrets.Versions.List(SecretPath(c.Project(), name)).
			Filter(enabledVersions).PageToken(token).Context(ctx).Do)
		if err != nil {
			return nil, fmt.Errorf("list the versions of the %s secret: %w", name, err)
		}
		for _, version := range page.Versions {
			number, err := versionNumber(version.Name)
			if err == nil && version.State == EnabledVersion && number < newest {
				older = append(older, numberedVersion{name: version.Name, number: number})
			}
		}
		if token = page.NextPageToken; token == "" {
			break
		}
	}
	slices.SortFunc(older, func(a, b numberedVersion) int { return b.number - a.number })
	return older, nil
}

func (c *Clients) destroyVersion(ctx context.Context, service *secretmanager.Service, name, version string) error {
	_, err := googleretry.Attempted(ctx, service.Projects.Secrets.Versions.Destroy(version, &secretmanager.DestroySecretVersionRequest{}).Context(ctx).Do)
	if err != nil && !Absent(err) && !unreadableVersion(err) {
		return fmt.Errorf("destroy %s, an older version of the %s secret: %w", version, name, err)
	}
	return nil
}

func versionNumber(name string) (int, error) {
	_, number, found := strings.Cut(name, "/versions/")
	if !found {
		return 0, fmt.Errorf("%q names no version", name)
	}
	return strconv.Atoi(number)
}

func (c *Clients) DeleteSecret(ctx context.Context, service *secretmanager.Service, tier environment.Tier, name string) error {
	if err := c.untagSecret(ctx, service, tier, name); err != nil {
		return err
	}
	if _, err := googleretry.Attempted(ctx, service.Projects.Secrets.Delete(SecretPath(c.Project(), name)).Context(ctx).Do); err != nil && !Absent(err) {
		return fmt.Errorf("delete the %s secret: %w", name, err)
	}
	return nil
}

func (c *Clients) untagSecret(ctx context.Context, service *secretmanager.Service, tier environment.Tier, name string) error {
	return c.RemoveTierTagIfFound(ctx, tier, func(ctx context.Context) (TaggedResource, bool, error) {
		_, err := googleretry.Attempted(ctx, service.Projects.Secrets.Get(SecretPath(c.Project(), name)).Context(ctx).Do)
		if Absent(err) {
			return TaggedResource{}, false, nil
		}
		if err != nil {
			return TaggedResource{}, false, fmt.Errorf("read the %s secret: %w", name, err)
		}
		tagged, err := c.SecretTagged(ctx, name)
		return tagged, err == nil, err
	})
}
