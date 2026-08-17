package edge

import (
	"context"
	"errors"
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

type RootStack interface {
	ReconcileRootStack(ctx context.Context, spec RootStackSpec, prior RootStackState) (RootStackState, error)

	StoreSchemaVersion(ctx context.Context, endpoint, slug string) (int, error)

	PutStaged(ctx context.Context, state RootStackState, record DeploymentRecord) error

	Promote(ctx context.Context, state RootStackState, promotion Promotion, pointer string) error

	History(ctx context.Context, state RootStackState, pointer string) ([]HistoryEntry, error)

	DeletePromotionArtifacts(ctx context.Context, state RootStackState, keepN int, pointer string) (PruneResult, error)

	RemovePointer(ctx context.Context, state RootStackState, pointer string) (PruneResult, error)

	RouteOwner(ctx context.Context, pattern string) (string, error)

	DestroyRootStack(ctx context.Context, workers []string) error

	ListDeployedWorkers(ctx context.Context, stem string) ([]string, error)

	DestroyInstance(ctx context.Context, state RootStackState) error
}

type RootStackSpec struct {
	Version             string
	GenericName         string
	Generic             Worker
	Slug                string
	StoreScriptName     string
	ISRWriterScriptName string
	StoreEndpoint       string
	BootstrapCred       string
	Domains             []string
	PruneOnly           bool
	PruneRoutes         bool
	PruneWorkerStem     string
	RequiredRecord      string
	Values              map[string]string
	Warn                func(string)
}

type RootStackState map[string]string

const (
	RootStackKeySlug       = "slug"
	RootStackKeyEndpoint   = "endpoint"
	RootStackKeySecret     = "secret"
	RootStackKeyOwnerToken = "ownerToken"
)

type DeploymentRecord struct {
	App              string            `json:"app"`
	Framework        string            `json:"framework"`
	Identity         string            `json:"identity"`
	DeploymentID     string            `json:"deploymentId"`
	BuildID          string            `json:"buildId"`
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
