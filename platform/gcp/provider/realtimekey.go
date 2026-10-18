package gcp

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"google.golang.org/api/secretmanager/v1"

	"github.com/ocelhq/ocel/pkg/refusal"
)

func (r realtimeEnvironment) secrets() (*secretmanager.Service, error) { return r.clients.Secrets() }

func (r realtimeEnvironment) signingSecret(resource string) string {
	return r.clients.RealtimeSigningSecret(r.ref.Project, r.ref.Name.Env, resource)
}

func (r realtimeEnvironment) readSigningSeed(ctx context.Context, service *secretmanager.Service, resource string) ([]byte, bool, error) {
	name := r.signingSecret(resource)
	seed, found, err := r.clients.readSecretVersion(ctx, service, name, firstSecretVersion)
	if err != nil || !found {
		return nil, false, err
	}
	if len(seed) != ed25519.SeedSize {
		return nil, false, refuseUnusableSeed(name, resource)
	}
	return seed, true, nil
}

func refuseUnusableSeed(name, resource string) error {
	return refusal.Refuse(refusal.CodeNotReady,
		"the first version of secret %s holds no Ed25519 signing key for realtime %s, and a realtime signs with the key its first version holds\n"+
			"Delete the secret and deploy again to mint a new one",
		name, resource)
}

func (r realtimeEnvironment) ensureSigningSeed(ctx context.Context, resource string) ([]byte, error) {
	service, err := r.secrets()
	if err != nil {
		return nil, err
	}
	seed, found, err := r.readSigningSeed(ctx, service, resource)
	if err != nil || found {
		return seed, err
	}
	name := r.signingSecret(resource)
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
	seed, found, err = r.readSigningSeed(ctx, service, resource)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, refuseUnusableSeed(name, resource)
	}
	return seed, nil
}

func (r realtimeEnvironment) removeSigningKey(ctx context.Context, resource string) error {
	service, err := r.secrets()
	if err != nil {
		return err
	}
	return r.clients.deleteSecret(ctx, service, r.signingSecret(resource))
}

type keysSecret struct {
	name    string
	written []byte
	found   bool
	current bool
}

func (r realtimeEnvironment) readKeysSecret(ctx context.Context, service *secretmanager.Service, keys map[string]string) (keysSecret, error) {
	secret := keysSecret{name: r.clients.RealtimeKeysSecret(r.ref.Tier, r.ref.Project, r.ref.Name.Env)}
	written, err := json.Marshal(keys)
	if err != nil {
		return keysSecret{}, err
	}
	secret.written = written
	current, found, err := r.clients.readSecretVersion(ctx, service, secret.name, "latest")
	if err != nil {
		return keysSecret{}, err
	}
	secret.found, secret.current = found, found && string(current) == string(written)
	return secret, nil
}

func (s keysSecret) describeChange(keys map[string]string) string {
	switch {
	case s.current:
		return ""
	case !s.found:
		return "the gateway's keys secret " + s.name + " is created to trust realtimes " + strings.Join(slices.Sorted(maps.Keys(keys)), ", ")
	}
	return "the gateway's keys secret " + s.name + " is given a new version trusting realtimes " + strings.Join(slices.Sorted(maps.Keys(keys)), ", ")
}

func (r realtimeEnvironment) writeKeys(ctx context.Context, keys map[string]string) error {
	service, err := r.secrets()
	if err != nil {
		return err
	}
	secret, err := r.readKeysSecret(ctx, service, keys)
	if err != nil || secret.current {
		return err
	}
	if !secret.found {
		if err := r.clients.ensureSecret(ctx, service, secret.name); err != nil {
			return err
		}
	}
	added, err := r.clients.addSecretVersion(ctx, service, secret.name, secret.written)
	if err != nil {
		return err
	}
	return r.clients.destroyVersionsBefore(ctx, service, secret.name, added)
}

func (r realtimeEnvironment) removeKeys(ctx context.Context) error {
	service, err := r.secrets()
	if err != nil {
		return err
	}
	return r.clients.deleteSecret(ctx, service, r.clients.RealtimeKeysSecret(r.ref.Tier, r.ref.Project, r.ref.Name.Env))
}
