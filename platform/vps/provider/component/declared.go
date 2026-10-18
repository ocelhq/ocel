package component

func ChooseDeclaredName(declared, name string) string {
	if declared != "" {
		return declared
	}
	return name
}
