package kvstore

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

const (
	ValkeyPort     = 6379
	ValkeyUsername = "ocel"
	ValkeyData     = "/data"
	ValkeyRunsAs   = "valkey:valkey"

	valkeySnapshots = "3600 1 300 100 60 10000"
)

type Valkey struct {
	Password    string
	MemoryBytes int64
	Eviction    string
}

func (v Valkey) Args() []string {
	hash := sha256.Sum256([]byte(v.Password))
	args := []string{"valkey-server",
		"--user", "default", "off",
		"--user", ValkeyUsername, "on", "#" + hex.EncodeToString(hash[:]), "~*", "&*", "+@all",
		"--maxmemory", strconv.FormatInt(v.MemoryBytes, 10),
	}
	if v.Eviction != "" {
		args = append(args, "--maxmemory-policy", v.Eviction)
	}
	return append(args,
		"--appendonly", "yes",
		"--appendfsync", "everysec",
		"--save", valkeySnapshots,
	)
}

func ValkeyReadyProbe() []string {
	return []string{"valkey-cli", "-e", "--no-auth-warning", "--user", ValkeyUsername, "--askpass", "ping"}
}

var ownedDirectives = []string{
	"user", "requirepass", "aclfile",
	"maxmemory", "maxmemory-policy",
	"appendonly", "appendfsync", "save", "dir", "dbfilename", "appenddirname", "appendfilename",
	"port", "bind", "protected-mode", "unixsocket", "tls-port",
	"cluster-enabled", "replicaof", "slaveof",
	"include", "enable-debug-command",
}

func RefuseOwnedArgs(args []string) error {
	for _, arg := range args {
		name, isDirective := strings.CutPrefix(arg, "--")
		if isDirective && slices.Contains(ownedDirectives, strings.ToLower(name)) {
			return fmt.Errorf("%s is a setting ocel renders for every store: drop it", arg)
		}
	}
	return nil
}
