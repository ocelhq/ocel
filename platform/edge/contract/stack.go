package edge

import (
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

type StackState map[string]string

const (
	StackKeySlug       = "slug"
	StackKeyEndpoint   = "endpoint"
	StackKeySecret     = "secret"
	StackKeyOwnerToken = "ownerToken"
	StackKeyClass      = "class"

	StackKeyProductionDomains = "productionDomains"
)

type DeploymentRecord struct {
	App              string            `json:"app"`
	Framework        string            `json:"framework"`
	Identity         string            `json:"identity"`
	DeploymentID     string            `json:"deploymentId"`
	BuildID          string            `json:"buildId"`
	Entry            string            `json:"entry"`
	EntryFunction    string            `json:"entryFunction,omitempty"`
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
