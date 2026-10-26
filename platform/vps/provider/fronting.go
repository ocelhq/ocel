package vps

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/seal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
)

const (
	frontingRecordName = "fronting"
	frontingPurpose    = "cloudflare fronting"
	frontingSecretSize = 32

	frontingRetirementLease = 2 * time.Minute
)

type frontingRecord struct {
	Credential   string    `json:"credential"`
	PurgeSeed    string    `json:"purgeSeed"`
	Store        string    `json:"store,omitempty"`
	Endpoint     string    `json:"endpoint,omitempty"`
	StoreVersion string    `json:"storeVersion,omitempty"`
	Projects     []string  `json:"projects,omitempty"`
	Retiring     time.Time `json:"retiring,omitzero"`
}

type fronting struct {
	store  edge.ReleasesStore
	record frontingRecord
	kept   cloudflare.KeptObjects
}

func frontingRecordKey(tier environment.Tier) records.Key {
	return stackrecords.EdgeStacksPartition(tier).Key(string(cloudflare.Kind), frontingRecordName)
}

func (p *Provider) keptObjects() (cloudflare.KeptObjects, error) {
	namespace, err := provider.NamespaceFromEnv()
	if err != nil {
		return cloudflare.KeptObjects{}, err
	}
	return cloudflare.NewKeptObjects(namespace.String(), p.cloudflareOrigin()), nil
}

func (p *Provider) holdFronting(ctx context.Context, tier environment.Tier, project string) (fronting, error) {
	record, err := p.claimFrontingRecord(ctx, tier, project)
	if err != nil {
		return fronting{}, err
	}
	kept, err := p.keptObjects()
	if err != nil {
		return fronting{}, err
	}
	if record.Endpoint == "" || record.StoreVersion != cloudflare.ComputeStoreVersion() {
		if record, err = p.installFrontingStore(ctx, tier, kept, record); err != nil {
			return fronting{}, err
		}
	}
	return fronting{store: record.releasesStore(), record: record, kept: kept}, nil
}

func (p *Provider) installFrontingStore(ctx context.Context, tier environment.Tier, kept cloudflare.KeptObjects, record frontingRecord) (frontingRecord, error) {
	install := kept.EnsureStore
	if p.installStore != nil {
		install = p.installStore
	}
	store, err := install(ctx, tier, record.Credential)
	if err != nil {
		return frontingRecord{}, err
	}
	return p.recordFrontingStore(ctx, tier, store)
}

func (r frontingRecord) releasesStore() edge.ReleasesStore {
	return edge.ReleasesStore{Name: r.Store, Endpoint: r.Endpoint, BootstrapCredential: r.Credential}
}

func (p *Provider) recordFrontingStore(ctx context.Context, tier environment.Tier, store edge.ReleasesStore) (frontingRecord, error) {
	var record frontingRecord
	err := records.Change(ctx, p.keyValues, frontingRecordKey(tier), func(entry records.Entry) ([]byte, bool, error) {
		opened, err := p.openFrontingRecord(ctx, tier, entry.Value)
		if err != nil {
			return nil, false, err
		}
		record = opened
		version := cloudflare.ComputeStoreVersion()
		if record.Store == store.Name && record.Endpoint == store.Endpoint && record.StoreVersion == version {
			return nil, false, nil
		}
		record.Store, record.Endpoint, record.StoreVersion = store.Name, store.Endpoint, version
		sealed, err := p.sealFrontingRecord(ctx, tier, record)
		return sealed, true, err
	})
	return record, err
}

func (p *Provider) forgetFrontingStoreVersion(ctx context.Context, tier environment.Tier) error {
	return records.Change(ctx, p.keyValues, frontingRecordKey(tier), func(entry records.Entry) ([]byte, bool, error) {
		if len(entry.Value) == 0 {
			return nil, false, nil
		}
		record, err := p.openFrontingRecord(ctx, tier, entry.Value)
		if err != nil || record.StoreVersion == "" {
			return nil, false, err
		}
		record.StoreVersion = ""
		sealed, err := p.sealFrontingRecord(ctx, tier, record)
		return sealed, true, err
	})
}

func (p *Provider) forgetFrontedProject(ctx context.Context, tier environment.Tier, project string) error {
	record, held, err := p.readFronting(ctx, tier)
	if err != nil || !held || !slices.Contains(record.Projects, project) {
		return err
	}
	kept, err := p.keptObjects()
	if err != nil {
		return err
	}
	prefixes := make([]string, 0, len(releaseObjectKinds))
	for _, kind := range releaseObjectKinds {
		prefixes = append(prefixes, string(kind)+naming.PathSeparator+project+naming.PathSeparator)
	}
	if err := kept.Remove(ctx, record.releasesStore(), project, prefixes); err != nil {
		return fmt.Errorf("remove what Cloudflare keeps of %s: %w", project, err)
	}
	var retiring time.Time
	err = p.changeFronting(ctx, tier, func(record *frontingRecord) bool {
		retiring = time.Time{}
		if !slices.Contains(record.Projects, project) {
			return false
		}
		record.Projects = slices.DeleteFunc(slices.Clone(record.Projects), func(held string) bool { return held == project })
		if len(record.Projects) == 0 {
			retiring = p.now()
			record.Retiring = retiring
		}
		return true
	})
	if err != nil || retiring.IsZero() {
		return err
	}
	return p.retireFronting(ctx, tier, retiring)
}

func (p *Provider) changeFronting(ctx context.Context, tier environment.Tier, change func(*frontingRecord) bool) error {
	return records.Change(ctx, p.keyValues, frontingRecordKey(tier), func(entry records.Entry) ([]byte, bool, error) {
		if len(entry.Value) == 0 {
			return nil, false, nil
		}
		record, err := p.openFrontingRecord(ctx, tier, entry.Value)
		if err != nil || !change(&record) {
			return nil, false, err
		}
		sealed, err := p.sealFrontingRecord(ctx, tier, record)
		return sealed, true, err
	})
}

func (p *Provider) readFronting(ctx context.Context, tier environment.Tier) (frontingRecord, bool, error) {
	entry, err := records.ReadOrEmpty(ctx, p.keyValues, frontingRecordKey(tier))
	if err != nil {
		return frontingRecord{}, false, err
	}
	if len(entry.Value) == 0 {
		return frontingRecord{}, false, nil
	}
	record, err := p.openFrontingRecord(ctx, tier, entry.Value)
	return record, err == nil, err
}

func (p *Provider) claimFrontingRecord(ctx context.Context, tier environment.Tier, project string) (frontingRecord, error) {
	var record frontingRecord
	err := records.Change(ctx, p.keyValues, frontingRecordKey(tier), func(entry records.Entry) ([]byte, bool, error) {
		claimed, changed, err := p.claimFronting(ctx, tier, project, entry.Value)
		record = claimed
		if err != nil || !changed {
			return nil, false, err
		}
		sealed, err := p.sealFrontingRecord(ctx, tier, claimed)
		return sealed, true, err
	})
	return record, err
}

func (p *Provider) claimFronting(ctx context.Context, tier environment.Tier, project string, value []byte) (frontingRecord, bool, error) {
	if len(value) > 0 {
		record, err := p.openFrontingRecord(ctx, tier, value)
		if err != nil {
			return frontingRecord{}, false, err
		}
		if record.Retiring.IsZero() {
			if project == "" || slices.Contains(record.Projects, project) {
				return record, false, nil
			}
			record.Projects = append(slices.Clone(record.Projects), project)
			return record, true, nil
		}
		if left := record.Retiring.Add(frontingRetirementLease).Sub(p.now()); left > 0 {
			return frontingRecord{}, false, refusal.Refuse(refusal.CodeNotReady,
				"another deploy is removing the store Cloudflare keeps for this box's %s tier\nDeploy again once it finishes, within %s",
				tier, left.Round(time.Second))
		}
	}
	minted, err := mintFrontingRecord()
	if err != nil {
		return frontingRecord{}, false, err
	}
	if project != "" {
		minted.Projects = []string{project}
	}
	return minted, true, nil
}

func mintFrontingRecord() (frontingRecord, error) {
	credential, err := mintFrontingSecret()
	if err != nil {
		return frontingRecord{}, err
	}
	seed, err := mintFrontingSecret()
	if err != nil {
		return frontingRecord{}, err
	}
	return frontingRecord{Credential: credential, PurgeSeed: seed}, nil
}

func mintFrontingSecret() (string, error) {
	buf := make([]byte, frontingSecretSize)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("mint the secret Cloudflare fronts this box with: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

func (p *Provider) sealFrontingRecord(ctx context.Context, tier environment.Tier, record frontingRecord) ([]byte, error) {
	plain, err := json.Marshal(record)
	if err != nil {
		return nil, err
	}
	sealed, err := p.cipher.Seal(ctx, tier, seal.Bind(frontingPurpose, string(tier)), plain)
	if err != nil {
		return nil, err
	}
	return json.Marshal(sealed)
}

func (p *Provider) openFrontingRecord(ctx context.Context, tier environment.Tier, value []byte) (frontingRecord, error) {
	var sealed []byte
	if err := json.Unmarshal(value, &sealed); err != nil {
		return frontingRecord{}, fmt.Errorf("read the record Cloudflare fronts this box's %s tier with: %w", tier, err)
	}
	plain, err := p.cipher.Open(ctx, tier, seal.Bind(frontingPurpose, string(tier)), sealed)
	if err != nil {
		return frontingRecord{}, fmt.Errorf("open the record Cloudflare fronts this box's %s tier with: %w", tier, err)
	}
	var record frontingRecord
	if err := json.Unmarshal(plain, &record); err != nil {
		return frontingRecord{}, fmt.Errorf("read the record Cloudflare fronts this box's %s tier with: %w", tier, err)
	}
	if record.Credential == "" || record.PurgeSeed == "" {
		return frontingRecord{}, errors.New("the record Cloudflare fronts this box with names no credential or purge seed")
	}
	return record, nil
}

func (p *Provider) forgetFronting(ctx context.Context, tier environment.Tier) error {
	var retiring time.Time
	err := p.changeFronting(ctx, tier, func(record *frontingRecord) bool {
		retiring = p.now()
		record.Retiring = retiring
		return true
	})
	if err != nil || retiring.IsZero() {
		return err
	}
	return p.retireFronting(ctx, tier, retiring)
}

func (p *Provider) retireFronting(ctx context.Context, tier environment.Tier, since time.Time) error {
	teardown := p.tearDownStore
	if teardown == nil {
		kept, err := p.keptObjects()
		if err != nil {
			return errors.Join(err, p.abandonRetirement(ctx, tier, since))
		}
		teardown = kept.Teardown
	}
	if err := teardown(ctx, tier); err != nil {
		return errors.Join(err, p.abandonRetirement(ctx, tier, since))
	}
	return records.ForgetMatching(ctx, p.keyValues, frontingRecordKey(tier), func(entry records.Entry) (bool, error) {
		record, err := p.openFrontingRecord(ctx, tier, entry.Value)
		return err == nil && record.Retiring.Equal(since), err
	})
}

func (p *Provider) abandonRetirement(ctx context.Context, tier environment.Tier, since time.Time) error {
	return p.changeFronting(ctx, tier, func(record *frontingRecord) bool {
		if !record.Retiring.Equal(since) {
			return false
		}
		record.Retiring = time.Time{}
		record.Store, record.Endpoint, record.StoreVersion = "", "", ""
		return true
	})
}
