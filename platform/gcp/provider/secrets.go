package gcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"google.golang.org/api/googleapi"
	"google.golang.org/api/secretmanager/v1"
)

const (
	firstSecretVersion = "1"
	enabledVersions    = "state:ENABLED"
	failedPrecondition = "FAILED_PRECONDITION"
)

func (c *clients) ensureSecret(ctx context.Context, service *secretmanager.Service, name string) error {
	_, err := attempted(ctx, service.Projects.Secrets.Create("projects/"+c.project, &secretmanager.Secret{
		Replication: &secretmanager.Replication{Automatic: &secretmanager.Automatic{}},
	}).SecretId(name).Context(ctx).Do)
	if err != nil && !taken(err) {
		return fmt.Errorf("create the %s secret: %w", name, err)
	}
	return nil
}

func (c *clients) readSecretVersion(ctx context.Context, service *secretmanager.Service, name, version string) ([]byte, bool, error) {
	read, err := attempted(ctx, service.Projects.Secrets.Versions.Access(secretPath(c.project, name)+"/versions/"+version).Context(ctx).Do)
	switch {
	case absent(err):
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

func (c *clients) addSecretVersion(ctx context.Context, service *secretmanager.Service, name string, payload []byte) (*secretmanager.SecretVersion, error) {
	added, err := attempted(ctx, service.Projects.Secrets.AddVersion(secretPath(c.project, name), &secretmanager.AddSecretVersionRequest{
		Payload: &secretmanager.SecretPayload{Data: base64.StdEncoding.EncodeToString(payload)},
	}).Context(ctx).Do)
	if err != nil {
		return nil, fmt.Errorf("write a version of the %s secret: %w", name, err)
	}
	return added, nil
}

func (c *clients) destroyVersionsBefore(ctx context.Context, service *secretmanager.Service, name string, kept *secretmanager.SecretVersion) error {
	newest, err := versionNumber(kept.Name)
	if err != nil {
		return fmt.Errorf("read the version of the %s secret just written: %w", name, err)
	}
	var superseded []string
	for token := ""; ; {
		page, err := attempted(ctx, service.Projects.Secrets.Versions.List(secretPath(c.project, name)).
			Filter(enabledVersions).PageToken(token).Context(ctx).Do)
		if err != nil {
			return fmt.Errorf("list the versions of the %s secret: %w", name, err)
		}
		for _, version := range page.Versions {
			number, err := versionNumber(version.Name)
			if err == nil && version.State == enabledVersion && number < newest {
				superseded = append(superseded, version.Name)
			}
		}
		if token = page.NextPageToken; token == "" {
			break
		}
	}
	for _, version := range superseded {
		if _, err := attempted(ctx, service.Projects.Secrets.Versions.Destroy(version, &secretmanager.DestroySecretVersionRequest{}).Context(ctx).Do); err != nil && !absent(err) && !unreadableVersion(err) {
			return fmt.Errorf("destroy %s, which a newer version of the %s secret replaces: %w", version, name, err)
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

func (c *clients) deleteSecret(ctx context.Context, service *secretmanager.Service, name string) error {
	if _, err := attempted(ctx, service.Projects.Secrets.Delete(secretPath(c.project, name)).Context(ctx).Do); err != nil && !absent(err) {
		return fmt.Errorf("delete the %s secret: %w", name, err)
	}
	return nil
}
