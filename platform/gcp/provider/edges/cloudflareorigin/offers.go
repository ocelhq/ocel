package cloudflareorigin

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
)

const (
	adoptedEdgeRecord = "bootstrap"
	purgeSeedLen      = 32
	latestVersion     = "latest"
)

type AdoptedEdge struct {
	ReleasesStore AdoptedWorker `json:"releasesStore"`
}

type AdoptedWorker struct {
	Endpoint   string `json:"endpoint"`
	ScriptName string `json:"scriptName"`
}

type EdgeCredentials struct {
	ReleasesStore             string `json:"releasesStore"`
	AssetStoreAccessKeyID     string `json:"assetStoreAccessKeyId,omitempty"`
	AssetStoreSecretAccessKey string `json:"assetStoreSecretAccessKey,omitempty"`
}

type Offers struct {
	OpenClients func(context.Context) (*gcloud.Clients, error)
	KeyValues   records.Store
}

func (p Offers) ReadPurgeSecret(ctx context.Context, tier environment.Tier, kind edge.Kind, storageCoordinate string) (string, error) {
	c, err := p.OpenClients(ctx)
	if err != nil {
		return "", err
	}
	if _, _, err := RequireAdoptedEdge(ctx, c, p.KeyValues, tier, kind); err != nil {
		return "", err
	}
	seed, err := ReadPurgeSeed(ctx, c, tier, kind)
	if err != nil {
		return "", err
	}
	return edge.DerivePurgeSecret(seed, storageCoordinate), nil
}

func (c EdgeCredentials) hasAssetStoreKey() bool {
	return c.AssetStoreAccessKeyID != "" && c.AssetStoreSecretAccessKey != ""
}

func AdoptedEdgeKey(tier environment.Tier, kind edge.Kind) records.Key {
	return stackrecords.EdgeStacksPartition(tier).Key(string(kind), adoptedEdgeRecord)
}

func AdoptOffers(
	ctx context.Context,
	c *gcloud.Clients,
	store records.Store,
	tier environment.Tier,
	kind edge.Kind,
	out edge.BootstrapOutput,
	runProgress progress.Log,
) error {
	offered := out.ReleasesStore
	if offered == nil {
		return nil
	}
	say := gcloud.EnsureProgress(runProgress)
	stored, storedCredentials, err := readAdoptedEdge(ctx, c, store, tier, kind)
	if err != nil {
		return err
	}
	say.Say(fmt.Sprintf("Adopting the %s edge's releases-store worker (Secret Manager)", kind))
	adopted := AdoptedEdge{ReleasesStore: AdoptedWorker{Endpoint: offered.Endpoint, ScriptName: offered.Name}}
	credentials := storedCredentials
	if offered.BootstrapCredential != "" {
		credentials.ReleasesStore = offered.BootstrapCredential
	}
	if credentials.ReleasesStore == "" {
		return edgeCredentialUnrecorded(c, tier, kind, "releases store", adopted.ReleasesStore.ScriptName)
	}
	if credentials != storedCredentials {
		if err := WriteEdgeCredentials(ctx, c, tier, kind, credentials); err != nil {
			return err
		}
	}
	if err := ensurePurgeSeed(ctx, c, tier, kind); err != nil {
		return err
	}
	if adopted == stored {
		return nil
	}
	return writeAdoptedEdge(ctx, store, tier, kind, adopted)
}

func edgeCredentialUnrecorded(c *gcloud.Clients, tier environment.Tier, kind edge.Kind, surface, scriptName string) error {
	return refusal.Refuse(refusal.CodeInvalid,
		"the %s edge reoffered its %s %q without a bootstrap credential, meaning it still has the one it was given, "+
			"but secret %[4]s stores none: a prior bootstrap of this origin set that credential and failed before storing it, "+
			"or secret %[4]s was deleted since. It cannot be read back, so delete the bootstrap credential set on %[3]q "+
			"at the %[1]s edge and re-run bootstrap to mint a fresh one",
		kind, surface, scriptName, c.EdgeCredentialsSecret(tier, kind))
}

func readAdoptedEdge(ctx context.Context, c *gcloud.Clients, store records.Store, tier environment.Tier, kind edge.Kind) (AdoptedEdge, EdgeCredentials, error) {
	var adopted AdoptedEdge
	entry, err := records.ReadOrEmpty(ctx, store, AdoptedEdgeKey(tier, kind))
	if err != nil {
		return AdoptedEdge{}, EdgeCredentials{}, fmt.Errorf("read the %s edge's adopted workers: %w", kind, err)
	}
	if len(entry.Value) > 0 {
		if err := json.Unmarshal(entry.Value, &adopted); err != nil {
			return AdoptedEdge{}, EdgeCredentials{}, fmt.Errorf("parse the %s edge's adopted workers: %w", kind, err)
		}
	}
	credentials, err := ReadEdgeCredentials(ctx, c, tier, kind)
	if err != nil {
		return AdoptedEdge{}, EdgeCredentials{}, err
	}
	return adopted, credentials, nil
}

func ReadEdgeCredentials(ctx context.Context, c *gcloud.Clients, tier environment.Tier, kind edge.Kind) (EdgeCredentials, error) {
	service, err := c.Secrets()
	if err != nil {
		return EdgeCredentials{}, err
	}
	name := c.EdgeCredentialsSecret(tier, kind)
	payload, found, err := c.ReadSecretVersion(ctx, service, name, latestVersion)
	if err != nil || !found {
		return EdgeCredentials{}, err
	}
	var credentials EdgeCredentials
	if err := json.Unmarshal(payload, &credentials); err != nil {
		return EdgeCredentials{}, fmt.Errorf("parse the %s secret: %w", name, err)
	}
	return credentials, nil
}

func WriteEdgeCredentials(ctx context.Context, c *gcloud.Clients, tier environment.Tier, kind edge.Kind, credentials EdgeCredentials) error {
	service, err := c.Secrets()
	if err != nil {
		return err
	}
	name := c.EdgeCredentialsSecret(tier, kind)
	payload, err := json.Marshal(credentials)
	if err != nil {
		return fmt.Errorf("marshal the %s edge's bootstrap credentials: %w", kind, err)
	}
	if err := c.EnsureSecret(ctx, service, tier, name); err != nil {
		return err
	}
	added, err := c.AddSecretVersion(ctx, service, name, payload)
	if err != nil {
		return err
	}
	return c.DestroyVersionsBefore(ctx, service, name, added)
}

func ensurePurgeSeed(ctx context.Context, c *gcloud.Clients, tier environment.Tier, kind edge.Kind) error {
	service, err := c.Secrets()
	if err != nil {
		return err
	}
	name := c.PurgeSeedSecret(tier, kind)
	if _, found, err := c.ReadSecretVersion(ctx, service, name, gcloud.FirstSecretVersion); err != nil || found {
		return err
	}
	if err := c.EnsureSecret(ctx, service, tier, name); err != nil {
		return err
	}
	minted := make([]byte, purgeSeedLen)
	if _, err := rand.Read(minted); err != nil {
		return fmt.Errorf("mint the %s edge's purge seed: %w", kind, err)
	}
	_, err = c.AddSecretVersion(ctx, service, name, []byte(hex.EncodeToString(minted)))
	return err
}

func writeAdoptedEdge(ctx context.Context, store records.Store, tier environment.Tier, kind edge.Kind, adopted AdoptedEdge) error {
	key := AdoptedEdgeKey(tier, kind)
	entry, err := records.ReadOrEmpty(ctx, store, key)
	if err != nil {
		return fmt.Errorf("read %s: %w", key, err)
	}
	if entry.Value, err = json.Marshal(adopted); err != nil {
		return fmt.Errorf("record %s: %w", key, err)
	}
	if _, err := store.Write(ctx, entry); err != nil {
		return fmt.Errorf("record %s: %w", key, err)
	}
	return nil
}

func notBootstrapped(tier environment.Tier, kind edge.Kind, missing string) error {
	return refusal.Refuse(refusal.CodeNotReady,
		"the %s edge's bootstrap left %s on record: run `%s` again so the edge's workers are adopted",
		kind, missing, provider.BootstrapCommand(tier))
}

func RequireAdoptedEdge(ctx context.Context, c *gcloud.Clients, store records.Store, tier environment.Tier, kind edge.Kind) (AdoptedEdge, EdgeCredentials, error) {
	adopted, credentials, err := readAdoptedEdge(ctx, c, store, tier, kind)
	if err != nil {
		return AdoptedEdge{}, EdgeCredentials{}, err
	}
	for _, missing := range []struct {
		what string
		got  string
	}{
		{"no releases-store endpoint", adopted.ReleasesStore.Endpoint},
		{"no releases-store script name", adopted.ReleasesStore.ScriptName},
		{"no releases-store credential", credentials.ReleasesStore},
	} {
		if missing.got == "" {
			return AdoptedEdge{}, EdgeCredentials{}, notBootstrapped(tier, kind, missing.what)
		}
	}
	return adopted, credentials, nil
}

func ReadPurgeSeed(ctx context.Context, c *gcloud.Clients, tier environment.Tier, kind edge.Kind) (string, error) {
	service, err := c.Secrets()
	if err != nil {
		return "", err
	}
	seed, found, err := c.ReadSecretVersion(ctx, service, c.PurgeSeedSecret(tier, kind), gcloud.FirstSecretVersion)
	if err != nil {
		return "", err
	}
	if !found || len(seed) == 0 {
		return "", notBootstrapped(tier, kind, "no purge seed")
	}
	return string(seed), nil
}

func ForgetOffers(ctx context.Context, c *gcloud.Clients, store records.Store, tier environment.Tier, kind edge.Kind) error {
	if err := records.Forget(ctx, store, AdoptedEdgeKey(tier, kind)); err != nil {
		return fmt.Errorf("forget the %s edge's adopted workers: %w", kind, err)
	}
	service, err := c.Secrets()
	if err != nil {
		return err
	}
	for _, name := range []string{c.EdgeCredentialsSecret(tier, kind), c.PurgeSeedSecret(tier, kind), c.OriginSecret(tier, kind)} {
		if err := c.DeleteSecret(ctx, service, tier, name); err != nil {
			return err
		}
	}
	return nil
}
