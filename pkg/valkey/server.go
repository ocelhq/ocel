package valkey

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

const (
	Port     = 6379
	Username = "ocel"
	DataDir  = "/data"
	RunsAs   = "valkey:valkey"

	snapshots = "3600 1 300 100 60 10000"
)

type Server struct {
	Password    string
	MemoryBytes int64
	Eviction    string
}

func (v Server) Args() []string {
	hash := sha256.Sum256([]byte(v.Password))
	args := []string{"valkey-server",
		"--user", "default", "off",
		"--user", Username, "on", "#" + hex.EncodeToString(hash[:]), "~*", "&*", "+@all",
		"--maxmemory", strconv.FormatInt(v.MemoryBytes, 10),
	}
	if v.Eviction != "" {
		args = append(args, "--maxmemory-policy", v.Eviction)
	}
	return append(args,
		"--appendonly", "yes",
		"--appendfsync", "everysec",
		"--save", snapshots,
	)
}

func ReadyProbe() []string {
	return []string{"valkey-cli", "-e", "--no-auth-warning", "--user", Username, "--askpass", "ping"}
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
