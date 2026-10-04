package docsurl

const Origin = "https://ocel.dev"

func FormatSchema(version string) string {
	return Origin + "/schema/" + version + "/ocel.schema.json"
}

func FormatErrorPage(code string) string {
	return Origin + "/docs/errors/" + code
}
