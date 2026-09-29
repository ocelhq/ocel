package progress

type Title struct {
	Started string
	Ended   string
}

type Verb struct {
	Doing string
	Done  string
}

func (v Verb) Title(object string) Title {
	return Title{Started: v.Doing + " " + object, Ended: v.Done + " " + object}
}

var (
	Attaching    = Verb{Doing: "Attaching", Done: "Attached"}
	Checking     = Verb{Doing: "Checking", Done: "Checked"}
	Deploying    = Verb{Doing: "Deploying", Done: "Deployed"}
	Destroying   = Verb{Doing: "Destroying", Done: "Destroyed"}
	Detaching    = Verb{Doing: "Detaching", Done: "Detached"}
	Installing   = Verb{Doing: "Installing", Done: "Installed"}
	Planning     = Verb{Doing: "Planning", Done: "Planned"}
	Provisioning = Verb{Doing: "Provisioning", Done: "Provisioned"}
	Pruning      = Verb{Doing: "Pruning", Done: "Pruned"}
	Reading      = Verb{Doing: "Reading", Done: "Read"}
	Reclaiming   = Verb{Doing: "Reclaiming", Done: "Reclaimed"}
	Reconciling  = Verb{Doing: "Reconciling", Done: "Reconciled"}
	Releasing    = Verb{Doing: "Releasing", Done: "Released"}
	Removing     = Verb{Doing: "Removing", Done: "Removed"}
	Serving      = Verb{Doing: "Serving", Done: "Served"}
	Switching    = Verb{Doing: "Switching", Done: "Switched"}
	Updating     = Verb{Doing: "Updating", Done: "Updated"}
)
