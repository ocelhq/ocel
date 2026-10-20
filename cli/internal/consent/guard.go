package consent

type GuardID string

const (
	GuardNewProject               GuardID = "new_project"
	GuardPersistentPreviewRemoval GuardID = "persistent_preview_removal"
	GuardBootstrapDowngrade       GuardID = "bootstrap_downgrade"
)

type Guard struct {
	ID       GuardID
	Finding  string
	Question string
	Action   string
}

func (g Guard) assumption() string {
	return g.Action + " without confirmation (nobody to ask)"
}
