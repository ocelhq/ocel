package docsurl

const Origin = "https://ocel.dev"

const Schema = Origin + "/schema/ocel.schema.json"

func FormatTelemetryPage() string {
	return Origin + "/docs/telemetry"
}

func FormatErrorPage(code string) string {
	return Origin + "/docs/errors/" + code
}
