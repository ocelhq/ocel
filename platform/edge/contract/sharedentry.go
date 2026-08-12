package edge

import "context"

const (
	SharedPreviewEntryScript = "ocel-preview-entry"

	PreviewGrammarMin uint32 = 1
	PreviewGrammarMax uint32 = 1

	RootStackKeyGlobalPreview = "globalPreviewDomain"
)

func ServedOnGlobalPreview(state RootStackState, baseDomain string) bool {
	return baseDomain != "" && state[RootStackKeyGlobalPreview] == baseDomain
}

type SharedEntrySpec struct {
	Version             string
	ScriptName          string
	Generic             Worker
	BaseDomain          string
	GrammarMin          uint32
	GrammarMax          uint32
	ISRWriterScriptName string
	Values              map[string]string
	Warn                func(string)
}

type SharedEntry interface {
	ReconcileSharedEntry(ctx context.Context, spec SharedEntrySpec) error

	DestroySharedEntry(ctx context.Context, baseDomain string) error
}

func SharedEntryWildcard(baseDomain string) string {
	if baseDomain == "" {
		return ""
	}
	return "*." + baseDomain
}
