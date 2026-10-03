package gcp

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"google.golang.org/api/googleapi"
	"google.golang.org/api/secretmanager/v1"

	"github.com/ocelhq/ocel/pkg/refusal"
)

const (
	realtimeKeysReaderRole = "roles/secretmanager.secretAccessor"
	firstSecretVersion     = "1"
	enabledVersions        = "state:ENABLED"
	failedPrecondition     = "FAILED_PRECONDITION"
)

func (r realtimeEnvironment) secrets() (*secretmanager.Service, error) { return r.clients.Secrets() }

func (r realtimeEnvironment) ensureSecret(ctx context.Context, service *secretmanager.Service, name string) error {
	_, err := attempted(ctx, service.Projects.Secrets.Create("projects/"+r.clients.project, &secretmanager.Secret{
		Replication: &secretmanager.Replication{Automatic: &secretmanager.Automatic{}},
	}).SecretId(name).Context(ctx).Do)
	if err != nil && !taken(err) {
		return fmt.Errorf("create the %s secret: %w", name, err)
	}
	return nil
}

func (r realtimeEnvironment) readSecretVersion(ctx context.Context, service *secretmanager.Service, name, version string) ([]byte, bool, error) {
	read, err := attempted(ctx, service.Projects.Secrets.Versions.Access(secretPath(r.clients.project, name)+"/versions/"+version).Context(ctx).Do)
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

func (r realtimeEnvironment) addSecretVersion(ctx context.Context, service *secretmanager.Service, name string, payload []byte) (*secretmanager.SecretVersion, error) {
	added, err := attempted(ctx, service.Projects.Secrets.AddVersion(secretPath(r.clients.project, name), &secretmanager.AddSecretVersionRequest{
		Payload: &secretmanager.SecretPayload{Data: base64.StdEncoding.EncodeToString(payload)},
	}).Context(ctx).Do)
	if err != nil {
		return nil, fmt.Errorf("write a version of the %s secret: %w", name, err)
	}
	return added, nil
}

func (r realtimeEnvironment) destroyVersionsBefore(ctx context.Context, service *secretmanager.Service, name string, kept *secretmanager.SecretVersion) error {
	newest, err := versionNumber(kept.Name)
	if err != nil {
		return fmt.Errorf("read the version of the %s secret just written: %w", name, err)
	}
	var superseded []string
	for token := ""; ; {
		page, err := attempted(ctx, service.Projects.Secrets.Versions.List(secretPath(r.clients.project, name)).
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

func (r realtimeEnvironment) deleteSecret(ctx context.Context, service *secretmanager.Service, name string) error {
	if _, err := attempted(ctx, service.Projects.Secrets.Delete(secretPath(r.clients.project, name)).Context(ctx).Do); err != nil && !absent(err) {
		return fmt.Errorf("delete the %s secret: %w", name, err)
	}
	return nil
}

func (r realtimeEnvironment) ensureSigningSeed(ctx context.Context, resource string) ([]byte, error) {
	service, err := r.secrets()
	if err != nil {
		return nil, err
	}
	name := r.clients.RealtimeSigningSecret(r.ref.Project, r.ref.Name.Env, resource)
	seed, found, err := r.readSecretVersion(ctx, service, name, firstSecretVersion)
	if err != nil {
		return nil, err
	}
	if !found {
		if err := r.ensureSecret(ctx, service, name); err != nil {
			return nil, err
		}
		minted := make([]byte, ed25519.SeedSize)
		if _, err := rand.Read(minted); err != nil {
			return nil, fmt.Errorf("mint a signing key for realtime %s: %w", resource, err)
		}
		if _, err := r.addSecretVersion(ctx, service, name, minted); err != nil {
			return nil, err
		}
		if seed, found, err = r.readSecretVersion(ctx, service, name, firstSecretVersion); err != nil {
			return nil, err
		}
	}
	if !found || len(seed) != ed25519.SeedSize {
		return nil, refusal.Refuse(refusal.CodeNotReady,
			"the first version of secret %s holds no Ed25519 signing key for realtime %s, and a realtime signs with the key its first version holds\n"+
				"Delete the secret and deploy again to mint a new one",
			name, resource)
	}
	return seed, nil
}

func (r realtimeEnvironment) removeSigningKey(ctx context.Context, resource string) error {
	service, err := r.secrets()
	if err != nil {
		return err
	}
	return r.deleteSecret(ctx, service, r.clients.RealtimeSigningSecret(r.ref.Project, r.ref.Name.Env, resource))
}

func (r realtimeEnvironment) writeKeys(ctx context.Context, keys map[string]string) error {
	service, err := r.secrets()
	if err != nil {
		return err
	}
	name := r.clients.RealtimeKeysSecret(r.ref.Project, r.ref.Name.Env)
	written, err := json.Marshal(keys)
	if err != nil {
		return err
	}
	current, found, err := r.readSecretVersion(ctx, service, name, "latest")
	if err != nil {
		return err
	}
	if !found {
		if err := r.ensureSecret(ctx, service, name); err != nil {
			return err
		}
	}
	if err := r.letGatewayReadKeys(ctx, service, name); err != nil {
		return err
	}
	if found && string(current) == string(written) {
		return nil
	}
	added, err := r.addSecretVersion(ctx, service, name, written)
	if err != nil {
		return err
	}
	return r.destroyVersionsBefore(ctx, service, name, added)
}

func (r realtimeEnvironment) letGatewayReadKeys(ctx context.Context, service *secretmanager.Service, name string) error {
	path := secretPath(r.clients.project, name)
	member := "serviceAccount:" + r.clients.RealtimeAccountEmail(r.ref.Tier)
	policy, err := attempted(ctx, service.Projects.Secrets.GetIamPolicy(path).Context(ctx).Do)
	if err != nil {
		return fmt.Errorf("read who may read the %s secret: %w", name, err)
	}
	bindings, changed := boundSecretMember(policy.Bindings, realtimeKeysReaderRole, member, true)
	if !changed {
		return nil
	}
	policy.Bindings = bindings
	if _, err := attempted(ctx, service.Projects.Secrets.SetIamPolicy(path, &secretmanager.SetIamPolicyRequest{Policy: policy}).Context(ctx).Do); err != nil {
		return fmt.Errorf("let %s read the %s secret: %w", member, name, err)
	}
	return nil
}

func (r realtimeEnvironment) removeKeys(ctx context.Context) error {
	service, err := r.secrets()
	if err != nil {
		return err
	}
	return r.deleteSecret(ctx, service, r.clients.RealtimeKeysSecret(r.ref.Project, r.ref.Name.Env))
}
