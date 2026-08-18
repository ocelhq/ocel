package edge

const PreviewEntryOwner = "ocel-preview-entry"

const (
	PreviewGrammarMin uint32 = 1
	PreviewGrammarMax uint32 = 1

	StackKeyGlobalPreview = "globalPreviewDomain"
)

func ServedOnGlobalPreview(state StackState, baseDomain string) bool {
	return baseDomain != "" && state[StackKeyGlobalPreview] == baseDomain
}

type PreviewWildcardSpec struct {
	Version     string
	BaseDomain  string
	Certificate string
	GrammarMin  uint32
	GrammarMax  uint32
	Values      map[string]string
	Warn        func(string)
	Program     *ProgramSpec
}

func PreviewWildcard(baseDomain string) string {
	if baseDomain == "" {
		return ""
	}
	return "*." + baseDomain
}
