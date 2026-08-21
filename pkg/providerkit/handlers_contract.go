package providerkit

import (
	"context"
	"fmt"

	connect "connectrpc.com/connect"

	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

// The twenty-two ProviderService handlers, with the ports each one reaches for.
// The bodies are not lifted yet — this file is the inventory that proves the kit
// can own the whole service, and the comment on each is the rule that made it
// kit-owned rather than a vendor's.
//
// The Substrate spellings below are the generated ones; #518 renames them.

func notLifted(rpc string) error {
	return connect.NewError(connect.CodeUnimplemented, fmt.Errorf("providerkit: %s is not lifted yet", rpc))
}

// Configure: decode the opaque options, hand them to Accept, keep them for the
// session. The refusal wording is the kit's.
func (h *handlers) Configure(context.Context, *contractv1.ConfigureRequest) (*contractv1.ConfigureResponse, error) {
	return nil, notLifted("Configure")
}

// Deploy: validate the manifest, gate it against Serves, resolve the class, name
// the stacks, upload through ArtifactStore, provision through Releaser, reconcile
// the edge, publish links, record through RecordStore, promote, warm if the
// Releaser is a Warmer. The one handler that touches nearly every port, and the
// reason Provision is a slice of it rather than the whole of it.
func (h *handlers) Deploy(context.Context, *contractv1.DeployRequest, *connect.ServerStream[progressv1.OperationEvent]) error {
	return notLifted("Deploy")
}

// Bootstrap: Bootstrapper.Apply, with the kit deciding what the request needs and
// whether this build would downgrade what is there, then the kit writes the root
// schema record.
func (h *handlers) Bootstrap(context.Context, *contractv1.BootstrapRequest, *connect.ServerStream[progressv1.OperationEvent]) error {
	return notLifted("Bootstrap")
}

// DescribeBootstrap: Bootstrapper.Describe, plus the staleness and downgrade
// rules over the facts it returned, and the root schema record — a tree older
// than this build is stale however current the vendor's own substrate is.
func (h *handlers) DescribeBootstrap(context.Context, *contractv1.DescribeBootstrapRequest) (*contractv1.DescribeBootstrapResponse, error) {
	return nil, notLifted("DescribeBootstrap")
}

// GetCredentialPolicy: Credentials.Policy, printed verbatim.
func (h *handlers) GetCredentialPolicy(context.Context, *contractv1.CredentialPolicyRequest) (*contractv1.CredentialPolicyResponse, error) {
	return nil, notLifted("GetCredentialPolicy")
}

// RemoveSubstrate: refuse if occupied, then Bootstrapper.Remove. The occupancy
// check is kit logic over RecordStore, so no vendor can forget it.
func (h *handlers) RemoveSubstrate(context.Context, *contractv1.SubstrateRequest, *connect.ServerStream[progressv1.OperationEvent]) error {
	return notLifted("RemoveSubstrate")
}

// PlanRemoveSubstrate: Bootstrapper.Removals plus the edge's surfaces, assembled
// into one plan. An empty plan stops before it destroys anything.
func (h *handlers) PlanRemoveSubstrate(context.Context, *contractv1.SubstrateRequest) (*contractv1.RemovalPlan, error) {
	return nil, notLifted("PlanRemoveSubstrate")
}

// RemoveEnvironment: the preview scope. Teardown order — project, then preview
// wildcard, then bootstrap, none cascading — is kit policy.
func (h *handlers) RemoveEnvironment(context.Context, *contractv1.RemoveEnvironmentRequest, *connect.ServerStream[progressv1.OperationEvent]) error {
	return notLifted("RemoveEnvironment")
}

// RemoveProject: Releaser.Destroy, ArtifactStore.RemovePrefix, ValueStore.Purge,
// RecordStore.Remove, and the edge's project surfaces, in that order.
func (h *handlers) RemoveProject(context.Context, *contractv1.ProjectRequest, *connect.ServerStream[progressv1.OperationEvent]) error {
	return notLifted("RemoveProject")
}

func (h *handlers) PlanRemoveProject(context.Context, *contractv1.ProjectRequest) (*contractv1.RemovalPlan, error) {
	return nil, notLifted("PlanRemoveProject")
}

// ListEnvironments: the project and stack records. No vendor call at all.
func (h *handlers) ListEnvironments(context.Context, *contractv1.ListEnvironmentsRequest) (*contractv1.ListEnvironmentsResponse, error) {
	return nil, notLifted("ListEnvironments")
}

// Preflight: Credentials.Whoami, Bootstrapper.Describe for both classes, and the
// domain gate policy — admit preview, admit production, probe all.
func (h *handlers) Preflight(context.Context, *contractv1.PreflightRequest) (*contractv1.PreflightResponse, error) {
	return nil, notLifted("Preflight")
}

// ListPromotions: the edge-stack record, the edge opened from it, then
// Ledger().History. The kit never reads promotions from records directly.
func (h *handlers) ListPromotions(context.Context, *contractv1.ListPromotionsRequest) (*contractv1.ListPromotionsResponse, error) {
	return nil, notLifted("ListPromotions")
}

// Rollback: the edge-stack record, then EdgeStack.Promote. The flip is the
// edge's compare-and-set — in the kit's ledger adapter or in the edge's own —
// which is the handler that made Write a compare-and-set.
func (h *handlers) Rollback(context.Context, *contractv1.RollbackRequest) (*contractv1.RollbackResponse, error) {
	return nil, notLifted("Rollback")
}

// RemoveStalePromotions: N is the kit's and so is the sweep, but the selection
// is Ledger().Prune per the contract. Each stale release's app stack then goes
// through Releaser.Destroy and each artifact prefix through
// ArtifactStore.RemovePrefix.
func (h *handlers) RemoveStalePromotions(context.Context, *contractv1.RemoveStalePromotionsRequest, *connect.ServerStream[progressv1.OperationEvent]) error {
	return notLifted("RemoveStalePromotions")
}

// UsePreviewWildcard: the settle loop over DNSRegistry and the edge, then a
// record. The refusal when a preview release still holds the wildcard is kit
// logic.
func (h *handlers) UsePreviewWildcard(context.Context, *contractv1.UsePreviewWildcardRequest, *connect.ServerStream[progressv1.OperationEvent]) error {
	return notLifted("UsePreviewWildcard")
}

func (h *handlers) GetPreviewWildcard(context.Context, *contractv1.PreviewWildcardRequest) (*contractv1.GetPreviewWildcardResponse, error) {
	return nil, notLifted("GetPreviewWildcard")
}

func (h *handlers) PlanRemovePreviewWildcard(context.Context, *contractv1.PreviewWildcardRequest) (*contractv1.RemovalPlan, error) {
	return nil, notLifted("PlanRemovePreviewWildcard")
}

func (h *handlers) RemovePreviewWildcard(context.Context, *contractv1.PreviewWildcardRequest, *connect.ServerStream[progressv1.OperationEvent]) error {
	return notLifted("RemovePreviewWildcard")
}

// AddHostname: the settle loop — certificate from the edge, records through
// DNSRegistry or owed to the user, poll until the resolver agrees, bind on the
// edge, checkpoint to RecordStore at every step. All of it kit-owned; the ports
// supply a writer and a table.
func (h *handlers) AddHostname(context.Context, *contractv1.HostnameRequest, *connect.ServerStream[progressv1.OperationEvent]) error {
	return notLifted("AddHostname")
}

func (h *handlers) RemoveHostname(context.Context, *contractv1.HostnameRequest, *connect.ServerStream[progressv1.OperationEvent]) error {
	return notLifted("RemoveHostname")
}

func (h *handlers) GetHostnameStatus(context.Context, *contractv1.HostnameRequest) (*contractv1.GetHostnameStatusResponse, error) {
	return nil, notLifted("GetHostnameStatus")
}
