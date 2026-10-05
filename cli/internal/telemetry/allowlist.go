package telemetry

var allowedEvents = []string{"command_completed"}

var allowedProperties = []string{
	"command",
	"flags",
	"exit_code",
	"error_code",
	"duration_ms",
	"json",
	"tty",
	"cli_version",
	"os",
	"arch",
	"agent",
	"ci",
	"$process_person_profile",
}
