package events

import "strings"

var pastTenses = map[string]string{
	"Adding":       "Added",
	"Applying":     "Applied",
	"Attaching":    "Attached",
	"Building":     "Built",
	"Checking":     "Checked",
	"Collecting":   "Collected",
	"Creating":     "Created",
	"Deploying":    "Deployed",
	"Destroying":   "Destroyed",
	"Detaching":    "Detached",
	"Enumerating":  "Enumerated",
	"Installing":   "Installed",
	"Listing":      "Listed",
	"Loading":      "Loaded",
	"Planning":     "Planned",
	"Pricing":      "Priced",
	"Promoting":    "Promoted",
	"Provisioning": "Provisioned",
	"Pruning":      "Pruned",
	"Pushing":      "Pushed",
	"Reading":      "Read",
	"Reconciling":  "Reconciled",
	"Releasing":    "Released",
	"Removing":     "Removed",
	"Sending":      "Sent",
	"Serving":      "Served",
	"Switching":    "Switched",
	"Updating":     "Updated",
	"Uploading":    "Uploaded",
	"Waiting":      "Waited",
	"Wrapping":     "Wrapped",
}

func pastTense(title string) string {
	verb, rest, _ := strings.Cut(title, " ")
	past, ok := pastTenses[verb]
	if !ok {
		return title
	}
	if rest == "" {
		return past
	}
	return past + " " + rest
}
