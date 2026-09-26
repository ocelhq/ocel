package gcp

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	run "google.golang.org/api/run/v2"
	"google.golang.org/api/secretmanager/v1"

	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const (
	connectorKeyDir       = "/var/run/ocel/connector"
	connectorKeyFile      = "key"
	connectorKeyPath      = connectorKeyDir + "/" + connectorKeyFile
	connectorKeyVolume    = "connector-key"
	connectorKeyRole      = "roles/secretmanager.secretAccessor"
	connectorPublicKeyEnv = "OCEL_CONNECTOR_PUBLIC_KEY"
)

func connectorKeyMount(secret string) secretMount {
	return secretMount{name: connectorKeyVolume, secret: secret, dir: connectorKeyDir, file: connectorKeyFile}
}

func keyPathed(config []byte, path string) ([]byte, error) {
	var named map[string]any
	if err := json.Unmarshal(config, &named); err != nil {
		return nil, refusal.Refuse(refusal.CodeInvalid,
			"the connector config this install includes is not an object: %s", err)
	}
	named["keyPath"] = path
	written, err := json.Marshal(named)
	if err != nil {
		return nil, err
	}
	return append(written, '\n'), nil
}

func keyPayload(seed []byte) []byte {
	return []byte(base64.StdEncoding.EncodeToString(seed) + "\n")
}

func publicKeyOf(payload []byte) (string, error) {
	seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(payload)))
	if err != nil {
		return "", fmt.Errorf("the connector key is not base64: %w", err)
	}
	if len(seed) != ed25519.SeedSize {
		return "", fmt.Errorf("the connector key is %d bytes, not %d", len(seed), ed25519.SeedSize)
	}
	public := ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
	return base64.StdEncoding.EncodeToString(public), nil
}

func connectorPublicKeyOf(service *run.GoogleCloudRunV2Service) string {
	if service.Template == nil {
		return ""
	}
	for _, container := range service.Template.Containers {
		for _, entry := range container.Env {
			if entry.Name == connectorPublicKeyEnv {
				return entry.Value
			}
		}
	}
	return ""
}

func (p *Provider) connectorKey(ctx context.Context, progress edge.Progress) (string, error) {
	clients, err := p.openClients(ctx)
	if err != nil {
		return "", err
	}
	service, err := clients.Secrets()
	if err != nil {
		return "", err
	}
	name := clients.ConnectorKeySecret()
	path := secretPath(clients.project, name)
	_, err = attempted(ctx, service.Projects.Secrets.Create("projects/"+clients.project, &secretmanager.Secret{
		Replication: &secretmanager.Replication{Automatic: &secretmanager.Automatic{}},
	}).SecretId(name).Context(ctx).Do)
	if err != nil && !taken(err) {
		return "", fmt.Errorf("create the %s secret the connector's key is kept in: %w", name, err)
	}

	latest, err := attempted(ctx, service.Projects.Secrets.Versions.Access(path+"/versions/latest").Context(ctx).Do)
	switch {
	case err == nil && latest.Payload != nil:
		payload, err := base64.StdEncoding.DecodeString(latest.Payload.Data)
		if err != nil {
			return "", fmt.Errorf("read the connector key %s stores: %w", name, err)
		}
		public, err := publicKeyOf(payload)
		if err != nil {
			return "", fmt.Errorf("read the connector key %s stores: %w", name, err)
		}
		say(progress, "The connector keeps the key "+name+" already stores")
		return public, nil
	case err != nil && !absent(err):
		return "", fmt.Errorf("read whether %s stores a connector key: %w", name, err)
	}

	seed := make([]byte, ed25519.SeedSize)
	if _, err := rand.Read(seed); err != nil {
		return "", fmt.Errorf("mint the connector's key: %w", err)
	}
	payload := keyPayload(seed)
	if _, err := attempted(ctx, service.Projects.Secrets.AddVersion(path, &secretmanager.AddSecretVersionRequest{
		Payload: &secretmanager.SecretPayload{Data: base64.StdEncoding.EncodeToString(payload)},
	}).Context(ctx).Do); err != nil {
		return "", fmt.Errorf("write the connector's key into %s: %w", name, err)
	}
	say(progress, "Minted the connector's key into "+name)
	return publicKeyOf(payload)
}

func (p *Provider) bindConnectorKeySecret(ctx context.Context, granting bool) error {
	clients, err := p.openClients(ctx)
	if err != nil {
		return err
	}
	service, err := clients.Secrets()
	if err != nil {
		return err
	}
	path := secretPath(clients.project, clients.ConnectorKeySecret())
	member := "serviceAccount:" + clients.ConnectorAccountEmail()
	policy, err := attempted(ctx, service.Projects.Secrets.GetIamPolicy(path).Context(ctx).Do)
	if absent(err) && !granting {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read who may read the connector's key: %w", err)
	}
	bindings, changed := boundSecretMember(policy.Bindings, connectorKeyRole, member, granting)
	if !changed {
		return nil
	}
	policy.Bindings = bindings
	if _, err := attempted(ctx, service.Projects.Secrets.SetIamPolicy(path,
		&secretmanager.SetIamPolicyRequest{Policy: policy}).Context(ctx).Do); err != nil {
		return fmt.Errorf("let %s read the connector's key: %w", member, err)
	}
	return nil
}

func boundSecretMember(bindings []*secretmanager.Binding, role, member string, granting bool) ([]*secretmanager.Binding, bool) {
	for _, binding := range bindings {
		if binding.Role != role {
			continue
		}
		members, changed := boundMembers(binding.Members, member, granting)
		binding.Members = members
		return bindings, changed
	}
	if !granting {
		return bindings, false
	}
	return append(bindings, &secretmanager.Binding{Role: role, Members: []string{member}}), true
}

func (p *Provider) takeConnectorKey(ctx context.Context, progress edge.Progress) error {
	clients, err := p.openClients(ctx)
	if err != nil {
		return err
	}
	service, err := clients.Secrets()
	if err != nil {
		return err
	}
	name := clients.ConnectorKeySecret()
	if _, err := attempted(ctx, service.Projects.Secrets.Delete(secretPath(clients.project, name)).Context(ctx).Do); err != nil && !absent(err) {
		return fmt.Errorf("delete the %s secret: %w", name, err)
	}
	say(progress, "Took away the connector's key")
	return nil
}
