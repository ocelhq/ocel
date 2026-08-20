package kit

import provider "github.com/ocelhq/ocel/platform/provider/contract"

const (
	StagePlan        provider.StageID = "plan"
	StageUpload      provider.StageID = "upload"
	StageEdge        provider.StageID = "edge"
	StageProvision   provider.StageID = "provision"
	StageLinks       provider.StageID = "links"
	StageDNS         provider.StageID = "dns"
	StagePromote     provider.StageID = "promote"
	StageBootstrap   provider.StageID = "bootstrap"
	StageTeardown    provider.StageID = "teardown"
	StageDestroy     provider.StageID = "destroy"
	StageCertificate provider.StageID = "certificate"
	StageBind        provider.StageID = "bind"
)

func DeployStages() []provider.Stage {
	return []provider.Stage{
		{ID: StagePlan, Title: "Planning"},
		{ID: StageUpload, Title: "Uploading artifacts"},
		{ID: StageEdge, Title: "Reconciling the edge"},
		{ID: StageProvision, Title: "Provisioning"},
		{ID: StageLinks, Title: "Publishing links"},
		{ID: StageDNS, Title: "Settling DNS"},
		{ID: StagePromote, Title: "Promoting"},
	}
}

func AppStage(app string) provider.StageID { return provider.StageID("provision.app:" + app) }

func Under(parent provider.StageID, id provider.StageID, title string) provider.Stage {
	return provider.Stage{ID: id, Title: title, Parent: parent}
}
