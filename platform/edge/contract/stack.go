package edge

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
)

func NameUnderStem(stem, name string) bool {
	if stem == "" || name == "" {
		return false
	}
	return name == stem || strings.HasPrefix(name, stem+"-")
}

const StoreSchemaVersion = 2

var ErrStoreSchemaUnreadable = errors.New("deployments store does not report a schema version")

var ErrStoreAbsent = errors.New("the deployments store is not provisioned")

type StackSpec struct {
	Version     string
	Class       Class
	Slug        string
	Domains     []string
	Values      map[string]string
	PruneOnly   bool
	PruneRoutes bool
	Warn        func(string)
	Program     *ProgramSpec
}

type ProgramSpec struct {
	Name                string
	Worker              Worker
	StoreScriptName     string
	ISRWriterScriptName string
	StoreEndpoint       string
	BootstrapCred       string
	PruneWorkerStem     string
	RequiredRecord      string
}

type StackState struct {
	Slug          string            `json:"slug,omitempty"`
	Class         Class             `json:"class,omitempty"`
	Endpoint      string            `json:"endpoint,omitempty"`
	Secret        string            `json:"secret,omitempty"`
	OwnerToken    string            `json:"ownerToken,omitempty"`
	Front         string            `json:"front,omitempty"`
	Fronts        map[string]string `json:"fronts,omitempty"`
	Bound         []string          `json:"bound,omitempty"`
	Records       []Record          `json:"records,omitempty"`
	GlobalPreview string            `json:"globalPreview,omitempty"`
	PreviewBase   string            `json:"previewBase,omitempty"`
	Adapter       Private           `json:"adapter,omitzero"`
}

func (s StackState) Empty() bool {
	return s.Slug == "" && s.Class == "" && s.Endpoint == "" && s.Secret == "" && s.OwnerToken == "" &&
		s.Front == "" && len(s.Fronts) == 0 && len(s.Bound) == 0 && len(s.Records) == 0 &&
		s.GlobalPreview == "" && s.PreviewBase == "" && s.Adapter.IsZero()
}

func (s StackState) Equal(other StackState) bool {
	return s.Slug == other.Slug &&
		s.Class == other.Class &&
		s.Endpoint == other.Endpoint &&
		s.Secret == other.Secret &&
		s.OwnerToken == other.OwnerToken &&
		s.Front == other.Front &&
		s.GlobalPreview == other.GlobalPreview &&
		s.PreviewBase == other.PreviewBase &&
		maps.Equal(s.Fronts, other.Fronts) &&
		slices.Equal(s.Bound, other.Bound) &&
		slices.Equal(s.Records, other.Records) &&
		s.Adapter.sameAs(other.Adapter)
}

type Private struct {
	value any
	raw   json.RawMessage
}

func Own(value any) Private { return Private{value: value} }

func (p Private) IsZero() bool { return p.value == nil && len(p.raw) == 0 }

func (p Private) Into(target any) error {
	raw, err := p.encoded()
	if err != nil || len(raw) == 0 {
		return err
	}
	return json.Unmarshal(raw, target)
}

func (p Private) encoded() (json.RawMessage, error) {
	if p.value == nil {
		return p.raw, nil
	}
	raw, err := json.Marshal(p.value)
	if err != nil {
		return nil, fmt.Errorf("serialize the state the edge keeps to itself: %w", err)
	}
	return raw, nil
}

func (p Private) MarshalJSON() ([]byte, error) {
	raw, err := p.encoded()
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return []byte("null"), nil
	}
	return raw, nil
}

func (p *Private) UnmarshalJSON(raw []byte) error {
	p.value, p.raw = nil, nil
	if !bytes.Equal(raw, []byte("null")) {
		p.raw = slices.Clone(raw)
	}
	return nil
}

func (p Private) sameAs(other Private) bool {
	mine, err := p.encoded()
	if err != nil {
		return false
	}
	theirs, err := other.encoded()
	if err != nil {
		return false
	}
	return bytes.Equal(mine, theirs)
}

type DeploymentRecord struct {
	App              string            `json:"app"`
	Runtime          string            `json:"runtime"`
	Identity         string            `json:"identity"`
	DeploymentID     string            `json:"deploymentId"`
	Entry            string            `json:"entry"`
	EntryFunction    string            `json:"entryFunction,omitempty"`
	Image            string            `json:"image,omitempty"`
	Physical         string            `json:"physical,omitempty"`
	HealthPath       string            `json:"healthPath,omitempty"`
	RoutingManifest  any               `json:"routingManifest"`
	FunctionURLs     map[string]string `json:"functionUrls"`
	AssetPrefix      string            `json:"assetPrefix"`
	IsrPrefix        string            `json:"isrPrefix"`
	IsrWriteSecret   string            `json:"isrWriteSecret,omitempty"`
	CreatedAt        int64             `json:"createdAt"`
	EdgeWorkers      *Code             `json:"edgeWorkers,omitempty"`
	ValueFingerprint string            `json:"valueFingerprint,omitempty"`
	Variables        []VariableRecord  `json:"variables,omitempty"`
	Env              map[string]string `json:"env,omitempty"`
	Envelope         string            `json:"envelope,omitempty"`
	Needs            []Need            `json:"needs,omitempty"`
	SupportInEffect  []Need            `json:"supportInEffect,omitempty"`
	Waived           []Need            `json:"waived,omitempty"`
}

var OwnedVariableNames = []string{"OCEL_CACHE_RPC", "OCEL_CACHE_SCOPE"}

type VariableRecord struct {
	Key     string `json:"key"`
	Folder  string `json:"folder,omitempty"`
	Version int64  `json:"version,omitempty"`
	Live    bool   `json:"live,omitempty"`
}

type Code struct {
	BundleKey   string   `json:"bundleKey"`
	ID          string   `json:"id"`
	CompatDate  string   `json:"compatDate"`
	CompatFlags []string `json:"compatFlags"`
}

type Promotion struct {
	PromotionID string            `json:"promotionId"`
	Ts          int64             `json:"ts"`
	Builds      map[string]string `json:"builds"`
	Tag         string            `json:"tag,omitempty"`
	Flip        *FlipBound        `json:"flip,omitempty"`
}

type HistoryEntry struct {
	Promotion
	Active bool `json:"active"`
}

type PruneResult struct {
	KeptPromotionIDs           []string `json:"keptPromotionIds"`
	RemovedPromotionIDs        []string `json:"removedPromotionIds"`
	RemovedRecordKeys          []string `json:"removedRecordKeys"`
	SurvivingRecordKeys        []string `json:"survivingRecordKeys"`
	SurvivingPointerRecordKeys []string `json:"survivingPointerRecordKeys"`
}
