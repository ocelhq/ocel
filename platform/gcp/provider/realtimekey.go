package gcp

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"

	"google.golang.org/api/secretmanager/v1"

	"github.com/ocelhq/ocel/pkg/refusal"
)

func (r realtimeEnvironment) secrets() (*secretmanager.Service, error) { return r.clients.Secrets() }

func (r realtimeEnvironment) ensureSigningSeed(ctx context.Context, resource string) ([]byte, error) {
	service, err := r.secrets()
	if err != nil {
		return nil, err
	}
	name := r.clients.RealtimeSigningSecret(r.ref.Project, r.ref.Name.Env, resource)
	seed, found, err := r.clients.readSecretVersion(ctx, service, name, firstSecretVersion)
	if err != nil {
		return nil, err
	}
	if !found {
		if err := r.clients.ensureSecret(ctx, service, name); err != nil {
			return nil, err
		}
		minted := make([]byte, ed25519.SeedSize)
		if _, err := rand.Read(minted); err != nil {
			return nil, fmt.Errorf("mint a signing key for realtime %s: %w", resource, err)
		}
		if _, err := r.clients.addSecretVersion(ctx, service, name, minted); err != nil {
			return nil, err
		}
		if seed, found, err = r.clients.readSecretVersion(ctx, service, name, firstSecretVersion); err != nil {
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
	return r.clients.deleteSecret(ctx, service, r.clients.RealtimeSigningSecret(r.ref.Project, r.ref.Name.Env, resource))
}

func (r realtimeEnvironment) writeKeys(ctx context.Context, keys map[string]string) error {
	service, err := r.secrets()
	if err != nil {
		return err
	}
	name := r.clients.RealtimeKeysSecret(r.ref.Tier, r.ref.Project, r.ref.Name.Env)
	written, err := json.Marshal(keys)
	if err != nil {
		return err
	}
	current, found, err := r.clients.readSecretVersion(ctx, service, name, "latest")
	if err != nil {
		return err
	}
	if !found {
		if err := r.clients.ensureSecret(ctx, service, name); err != nil {
			return err
		}
	}
	if found && string(current) == string(written) {
		return nil
	}
	added, err := r.clients.addSecretVersion(ctx, service, name, written)
	if err != nil {
		return err
	}
	return r.clients.destroyVersionsBefore(ctx, service, name, added)
}

func (r realtimeEnvironment) removeKeys(ctx context.Context) error {
	service, err := r.secrets()
	if err != nil {
		return err
	}
	return r.clients.deleteSecret(ctx, service, r.clients.RealtimeKeysSecret(r.ref.Tier, r.ref.Project, r.ref.Name.Env))
}
