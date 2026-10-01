package kvstore

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

const (
	DefaultMemory  = "256mb"
	MinMemoryBytes = 32 << 20
	MaxMemoryBytes = 32 << 30
)

var memoryUnits = map[string]int64{"kb": 1 << 10, "mb": 1 << 20, "gb": 1 << 30}

var memorySize = regexp.MustCompile(`^([0-9]+)(kb|mb|gb)$`)

func ParseMemory(written string) (int64, error) {
	match := memorySize.FindStringSubmatch(strings.ToLower(written))
	if match == nil {
		return 0, fmt.Errorf("memory is %q, and a size is a whole number followed by kb, mb or gb, such as %q", written, DefaultMemory)
	}
	count, err := strconv.ParseInt(match[1], 10, 64)
	if err != nil || count > MaxMemoryBytes/memoryUnits[match[2]] {
		return 0, memoryOutOfRange(written)
	}
	bytes := count * memoryUnits[match[2]]
	if bytes < MinMemoryBytes || bytes > MaxMemoryBytes {
		return 0, memoryOutOfRange(written)
	}
	return bytes, nil
}

func memoryOutOfRange(written string) error {
	return fmt.Errorf("memory is %s, and a store holds from 32mb to 32gb", written)
}
