package tracing

import (
	"fmt"
	"os"
)

const ModeEnvVar = "OCEL_TRACE"

type Mode string

const (
	ModeOn   Mode = "on"
	ModeOff  Mode = "off"
	ModeFull Mode = "full"
)

func ParseMode(value string) (Mode, error) {
	switch Mode(value) {
	case "", ModeOn:
		return ModeOn, nil
	case ModeOff, ModeFull:
		return Mode(value), nil
	}
	return "", fmt.Errorf("%s=%q is not a trace mode: want %s, %s, %s or empty", ModeEnvVar, value, ModeOn, ModeOff, ModeFull)
}

func ReadMode() Mode {
	mode, err := ParseMode(os.Getenv(ModeEnvVar))
	if err != nil {
		return ModeOn
	}
	return mode
}
