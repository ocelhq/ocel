package gcp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

const (
	adoptedEdgeRecord = "bootstrap"
	purgeSeedLen      = 32
	latestVersion     = "latest"
)

type adoptedEdge struct {
	ReleasesStore     adoptedWorker            `json:"releasesStore"`
	ClientCertificate adoptedClientCertificate `json:"clientCertificate"`
}

type adoptedWorker struct {
	Endpoint   string `json:"endpoint"`
	ScriptName string `json:"scriptName"`
}

type adoptedClientCertificate struct {
	ID          string `json:"id"`
	Authorities string `json:"authorities"`
}

type edgeCredentials struct {
	ReleasesStore             string `json:"releasesStore"`
	AssetStoreAccessKeyID     string `json:"assetStoreAccessKeyId,omitempty"`
	AssetStoreSecretAccessKey string `json:"assetStoreSecretAccessKey,omitempty"`
}

func (c edgeCredentials) hasAssetStoreKey() bool {
	return c.AssetStoreAccessKeyID != "" && c.AssetStoreSecretAccessKey != ""
}

func adoptedEdgeKey(tier environment.Tier, kind edge.Kind) keyvalue.Key {
	return stackrecords.EdgeStacksPartition(tier).Key(string(kind), adoptedEdgeRecord)
}

func adoptEdgeOffers(
	ctx context.Context,
	c *clients,
	records keyvalue.Store,
	tier environment.Tier,
	kind edge.Kind,
	out edge.BootstrapOutput,
	runProgress progress.Log,
) error {
	if len(out.Offers) == 0 {
		return nil
	}
	say := ensureProgress(runProgress)
	stored, storedCredentials, err := readAdoptedEdge(ctx, c, records, tier, kind)
	if err != nil {
		return err
	}
	adopted, credentials := stored, storedCredentials
	sawReleasesStore := false
	for _, offer := range out.Offers {
		switch offer.Kind {
		case edge.OfferReleasesStore:
			say.Say(fmt.Sprintf("Adopting the %s edge's releases-store worker (Secret Manager)", kind))
			sawReleasesStore = true
			adopted.ReleasesStore = adoptedWorker{
				Endpoint:   offer.Values[edge.OfferKeyStoreEndpoint],
				ScriptName: offer.Values[edge.OfferKeyStoreScriptName],
			}
			if offered := offer.Values[edge.OfferKeyStoreBootstrapCredential]; offered != "" {
				credentials.ReleasesStore = offered
			}
			if credentials.ReleasesStore == "" {
				return edgeCredentialUnrecorded(c, tier, kind, "releases store", adopted.ReleasesStore.ScriptName)
			}
		case edge.OfferWorkerClientCertificate:
			adopted.ClientCertificate = adoptedClientCertificate{
				ID:          offer.Values[edge.OfferKeyClientCertificateID],
				Authorities: offer.Values[edge.OfferKeyClientCertificateAuthorities],
			}
		default:
			say.Warn(fmt.Sprintf("Ignoring the %s edge's %q offer: nothing in this bootstrap adopts it", kind, offer.Kind))
		}
	}
	if credentials != storedCredentials {
		if err := writeEdgeCredentials(ctx, c, tier, kind, credentials); err != nil {
			return err
		}
	}
	if sawReleasesStore {
		if err := ensurePurgeSeed(ctx, c, tier, kind); err != nil {
			return err
		}
	}
	if reflect.DeepEqual(adopted, stored) {
		return nil
	}
	return writeAdoptedEdge(ctx, records, tier, kind, adopted)
}

func edgeCredentialUnrecorded(c *clients, tier environment.Tier, kind edge.Kind, surface, scriptName string) error {
	return refusal.Refuse(refusal.CodeInvalid,
		"the %s edge reoffered its %s %q without a bootstrap credential, meaning it still has the one it was given, "+
			"but secret %s stores none: a prior bootstrap set that credential and failed before storing it. It cannot be read "+
			"back, so delete the bootstrap credential set on %q at the %s edge and re-run bootstrap to mint a fresh one",
		kind, surface, scriptName, c.EdgeCredentialsSecret(tier, kind), scriptName, kind)
}

func readAdoptedEdge(ctx context.Context, c *clients, records keyvalue.Store, tier environment.Tier, kind edge.Kind) (adoptedEdge, edgeCredentials, error) {
	var adopted adoptedEdge
	entry, err := keyvalue.ReadOrEmpty(ctx, records, adoptedEdgeKey(tier, kind))
	if err != nil {
		return adoptedEdge{}, edgeCredentials{}, fmt.Errorf("read the %s edge's adopted workers: %w", kind, err)
	}
	if len(entry.Value) > 0 {
		if err := json.Unmarshal(entry.Value, &adopted); err != nil {
			return adoptedEdge{}, edgeCredentials{}, fmt.Errorf("parse the %s edge's adopted workers: %w", kind, err)
		}
	}
	credentials, err := readEdgeCredentials(ctx, c, tier, kind)
	if err != nil {
		return adoptedEdge{}, edgeCredentials{}, err
	}
	return adopted, credentials, nil
}

func readEdgeCredentials(ctx context.Context, c *clients, tier environment.Tier, kind edge.Kind) (edgeCredentials, error) {
	service, err := c.Secrets()
	if err != nil {
		return edgeCredentials{}, err
	}
	name := c.EdgeCredentialsSecret(tier, kind)
	payload, found, err := c.readSecretVersion(ctx, service, name, latestVersion)
	if err != nil || !found {
		return edgeCredentials{}, err
	}
	var credentials edgeCredentials
	if err := json.Unmarshal(payload, &credentials); err != nil {
		return edgeCredentials{}, fmt.Errorf("parse the %s secret: %w", name, err)
	}
	return credentials, nil
}

func writeEdgeCredentials(ctx context.Context, c *clients, tier environment.Tier, kind edge.Kind, credentials edgeCredentials) error {
	service, err := c.Secrets()
	if err != nil {
		return err
	}
	name := c.EdgeCredentialsSecret(tier, kind)
	payload, err := json.Marshal(credentials)
	if err != nil {
		return fmt.Errorf("marshal the %s edge's bootstrap credentials: %w", kind, err)
	}
	if err := c.ensureSecret(ctx, service, name); err != nil {
		return err
	}
	added, err := c.addSecretVersion(ctx, service, name, payload)
	if err != nil {
		return err
	}
	return c.destroyVersionsBefore(ctx, service, name, added)
}

func ensurePurgeSeed(ctx context.Context, c *clients, tier environment.Tier, kind edge.Kind) error {
	service, err := c.Secrets()
	if err != nil {
		return err
	}
	name := c.PurgeSeedSecret(tier, kind)
	if _, found, err := c.readSecretVersion(ctx, service, name, firstSecretVersion); err != nil || found {
		return err
	}
	if err := c.ensureSecret(ctx, service, name); err != nil {
		return err
	}
	minted := make([]byte, purgeSeedLen)
	if _, err := rand.Read(minted); err != nil {
		return fmt.Errorf("mint the %s edge's purge seed: %w", kind, err)
	}
	_, err = c.addSecretVersion(ctx, service, name, []byte(hex.EncodeToString(minted)))
	return err
}

func writeAdoptedEdge(ctx context.Context, records keyvalue.Store, tier environment.Tier, kind edge.Kind, adopted adoptedEdge) error {
	key := adoptedEdgeKey(tier, kind)
	entry, err := keyvalue.ReadOrEmpty(ctx, records, key)
	if err != nil {
		return fmt.Errorf("read %s: %w", key, err)
	}
	if entry.Value, err = json.Marshal(adopted); err != nil {
		return fmt.Errorf("record %s: %w", key, err)
	}
	if _, err := records.Write(ctx, entry); err != nil {
		return fmt.Errorf("record %s: %w", key, err)
	}
	return nil
}

func notBootstrapped(tier environment.Tier, kind edge.Kind, missing string) error {
	return refusal.Refuse(refusal.CodeNotReady,
		"the %s edge's bootstrap left %s on record: run `%s` again so the edge's workers are adopted",
		kind, missing, provider.BootstrapCommand(tier))
}

func requireAdoptedEdge(ctx context.Context, c *clients, records keyvalue.Store, tier environment.Tier, kind edge.Kind) (adoptedEdge, edgeCredentials, error) {
	adopted, credentials, err := readAdoptedEdge(ctx, c, records, tier, kind)
	if err != nil {
		return adoptedEdge{}, edgeCredentials{}, err
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
			return adoptedEdge{}, edgeCredentials{}, notBootstrapped(tier, kind, missing.what)
		}
	}
	return adopted, credentials, nil
}

func readPurgeSeed(ctx context.Context, c *clients, tier environment.Tier, kind edge.Kind) (string, error) {
	service, err := c.Secrets()
	if err != nil {
		return "", err
	}
	seed, found, err := c.readSecretVersion(ctx, service, c.PurgeSeedSecret(tier, kind), firstSecretVersion)
	if err != nil {
		return "", err
	}
	if !found || len(seed) == 0 {
		return "", notBootstrapped(tier, kind, "no purge seed")
	}
	return string(seed), nil
}

func forgetEdgeOffers(ctx context.Context, c *clients, records keyvalue.Store, tier environment.Tier, kind edge.Kind) error {
	if err := keyvalue.Forget(ctx, records, adoptedEdgeKey(tier, kind)); err != nil {
		return fmt.Errorf("forget the %s edge's adopted workers: %w", kind, err)
	}
	service, err := c.Secrets()
	if err != nil {
		return err
	}
	for _, name := range []string{c.EdgeCredentialsSecret(tier, kind), c.PurgeSeedSecret(tier, kind)} {
		if err := c.deleteSecret(ctx, service, name); err != nil {
			return err
		}
	}
	return nil
}
