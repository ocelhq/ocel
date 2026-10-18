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

	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
)

const (
	FirstSecretVersion = "1"
	enabledVersions    = "state:ENABLED"
	failedPrecondition = "FAILED_PRECONDITION"
)

func (c *Clients) EnsureSecret(ctx context.Context, service *secretmanager.Service, name string) error {
	_, err := googleretry.Attempted(ctx, service.Projects.Secrets.Create("projects/"+c.Project(), &secretmanager.Secret{
		Replication: &secretmanager.Replication{Automatic: &secretmanager.Automatic{}},
	}).SecretId(name).Context(ctx).Do)
	if err != nil && !Taken(err) {
		return fmt.Errorf("create the %s secret: %w", name, err)
	}
	return nil
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
	type numbered struct {
		name   string
		number int
	}
	var older []numbered
	for token := ""; ; {
		page, err := googleretry.Attempted(ctx, service.Projects.Secrets.Versions.List(SecretPath(c.Project(), name)).
			Filter(enabledVersions).PageToken(token).Context(ctx).Do)
		if err != nil {
			return fmt.Errorf("list the versions of the %s secret: %w", name, err)
		}
		for _, version := range page.Versions {
			number, err := versionNumber(version.Name)
			if err == nil && version.State == EnabledVersion && number < newest {
				older = append(older, numbered{name: version.Name, number: number})
			}
		}
		if token = page.NextPageToken; token == "" {
			break
		}
	}
	slices.SortFunc(older, func(a, b numbered) int { return b.number - a.number })
	for _, version := range older[min(spared, len(older)):] {
		if _, err := googleretry.Attempted(ctx, service.Projects.Secrets.Versions.Destroy(version.name, &secretmanager.DestroySecretVersionRequest{}).Context(ctx).Do); err != nil && !Absent(err) && !unreadableVersion(err) {
			return fmt.Errorf("destroy %s, which a newer version of the %s secret replaces: %w", version.name, name, err)
		}
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

func (c *Clients) DeleteSecret(ctx context.Context, service *secretmanager.Service, name string) error {
	if _, err := googleretry.Attempted(ctx, service.Projects.Secrets.Delete(SecretPath(c.Project(), name)).Context(ctx).Do); err != nil && !Absent(err) {
		return fmt.Errorf("delete the %s secret: %w", name, err)
	}
	return nil
}
