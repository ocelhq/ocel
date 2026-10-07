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
	Adding       = Verb{Doing: "Adding", Done: "Added"}
	Assembling   = Verb{Doing: "Assembling", Done: "Assembled"}
	Attaching    = Verb{Doing: "Attaching", Done: "Attached"}
	Building     = Verb{Doing: "Building", Done: "Built"}
	Checking     = Verb{Doing: "Checking", Done: "Checked"}
	Collecting   = Verb{Doing: "Collecting", Done: "Collected"}
	Creating     = Verb{Doing: "Creating", Done: "Created"}
	Deploying    = Verb{Doing: "Deploying", Done: "Deployed"}
	Destroying   = Verb{Doing: "Destroying", Done: "Destroyed"}
	Detaching    = Verb{Doing: "Detaching", Done: "Detached"}
	Enumerating  = Verb{Doing: "Enumerating", Done: "Enumerated"}
	Forwarding   = Verb{Doing: "Forwarding", Done: "Forwarded"}
	Installing   = Verb{Doing: "Installing", Done: "Installed"}
	Loading      = Verb{Doing: "Loading", Done: "Loaded"}
	Planning     = Verb{Doing: "Planning", Done: "Planned"}
	Pricing      = Verb{Doing: "Pricing", Done: "Priced"}
	Provisioning = Verb{Doing: "Provisioning", Done: "Provisioned"}
	Pruning      = Verb{Doing: "Pruning", Done: "Pruned"}
	Reading      = Verb{Doing: "Reading", Done: "Read"}
	Reclaiming   = Verb{Doing: "Reclaiming", Done: "Reclaimed"}
	Reconciling  = Verb{Doing: "Reconciling", Done: "Reconciled"}
	Releasing    = Verb{Doing: "Releasing", Done: "Released"}
	Removing     = Verb{Doing: "Removing", Done: "Removed"}
	Running      = Verb{Doing: "Running", Done: "Ran"}
	Serving      = Verb{Doing: "Serving", Done: "Served"}
	Switching    = Verb{Doing: "Switching", Done: "Switched"}
	Updating     = Verb{Doing: "Updating", Done: "Updated"}
	Waiting      = Verb{Doing: "Waiting", Done: "Waited"}
)
