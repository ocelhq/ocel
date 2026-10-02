package realtime

import (
	"fmt"
	"time"
)

const (
	MinTokenTTL     = 10 * time.Second
	MaxTokenTTL     = 5 * time.Minute
	DefaultTokenTTL = 60 * time.Second
)

func refuseTokenTTL(ttl time.Duration) error {
	if ttl < MinTokenTTL || ttl > MaxTokenTTL {
		return fmt.Errorf("token ttl %s is outside %s to %s: a token lives long enough to open a socket and no longer", ttl, MinTokenTTL, MaxTokenTTL)
	}
	return nil
}

func refuseNamespace(name string) error {
	if !channelSegment.MatchString(name) {
		return fmt.Errorf("name %q is no channel namespace: the name begins every channel, so it is %s", name, segmentRule)
	}
	return nil
}
