package edge

const PreviewEntryOwner = "ocel-preview-entry"

func (s StackState) ServedOnGlobalPreview(baseDomain string) bool {
	return baseDomain != "" && s.GlobalPreview == baseDomain
}

type PreviewWildcardSpec struct {
	Version     string
	BaseDomain  string
	Certificate string
	Values      map[string]string
	Warn        func(string)
	Program     *ProgramSpec
	Origin      *Origin
}

func PreviewWildcard(baseDomain string) string {
	if baseDomain == "" {
		return ""
	}
	return "*." + baseDomain
}
