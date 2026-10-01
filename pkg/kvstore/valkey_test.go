package kvstore

import (
	"slices"
	"strings"
	"testing"
)

const hunter2SHA256 = "f52fbd32b2b3b86ff88ef6c490628285f482af15ddcb29541f94bcf526a3f6c7"

func directive(args []string, name string) []string {
	at := slices.Index(args, "--"+name)
	if at < 0 {
		return nil
	}
	end := at + 1
	for end < len(args) && !strings.HasPrefix(args[end], "--") {
		end++
	}
	return args[at+1 : end]
}

func TestAValkeyRunsWithTheDefaultUserOffAndOneUserKnownOnlyByItsPasswordsHash(t *testing.T) {
	t.Parallel()

	args := Valkey{Password: "hunter2", MemoryBytes: 256 << 20}.Args()
	if args[0] != "valkey-server" {
		t.Fatalf("Args() = %v, want the server as the command", args)
	}
	var users [][]string
	for at, arg := range args {
		if arg == "--user" {
			users = append(users, directive(args[at:], "user"))
		}
	}
	want := [][]string{{"default", "off"}, {ValkeyUsername, "on", "#" + hunter2SHA256, "~*", "&*", "+@all"}}
	if len(users) != 2 || !slices.Equal(users[0], want[0]) || !slices.Equal(users[1], want[1]) {
		t.Errorf("users = %v, want %v", users, want)
	}
	if strings.Contains(strings.Join(args, " "), "hunter2 ") || slices.Contains(args, "hunter2") || slices.Contains(args, ">hunter2") {
		t.Errorf("Args() = %v, and the password rides the command line docker inspect and ps show", args)
	}
}

func TestAValkeyHoldsItsMemoryAndPersistsEverySecondPlusSnapshots(t *testing.T) {
	t.Parallel()

	args := Valkey{Password: "p", MemoryBytes: 256 << 20, Eviction: "allkeys-lru"}.Args()
	for name, want := range map[string][]string{
		"maxmemory":        {"268435456"},
		"maxmemory-policy": {"allkeys-lru"},
		"appendonly":       {"yes"},
		"appendfsync":      {"everysec"},
		"save":             {"3600 1 300 100 60 10000"},
	} {
		if got := directive(args, name); !slices.Equal(got, want) {
			t.Errorf("--%s = %v, want %v", name, got, want)
		}
	}
}

func TestAValkeyNamingNoEvictionLeavesTheEnginesOwnDefault(t *testing.T) {
	t.Parallel()

	if args := (Valkey{Password: "p", MemoryBytes: 32 << 20}).Args(); slices.Contains(args, "--maxmemory-policy") {
		t.Errorf("Args() = %v, want no eviction policy when the store names none", args)
	}
}

func TestTheReadyProbeAuthenticatesWithAPasswordReadFromStdin(t *testing.T) {
	t.Parallel()

	probe := ValkeyReadyProbe()
	joined := strings.Join(probe, " ")
	for _, want := range []string{"valkey-cli", "-e", "--user " + ValkeyUsername, "--askpass", "ping"} {
		if !strings.Contains(joined, want) {
			t.Errorf("ValkeyReadyProbe() = %v, want it to include %q", probe, want)
		}
	}
}

func TestArgsSettingWhatTheStoreRendersAreRefused(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{
		{"--maxmemory", "1gb"},
		{"--io-threads", "4", "--USER", "default", "on", "nopass"},
		{"--requirepass", "x"},
		{"--appendonly", "no"},
		{"--aclfile", "/data/users.acl"},
		{"--dir", "/tmp"},
		{"--port", "6380"},
		{"--protected-mode", "no"},
		{"--include", "/etc/valkey.conf"},
		{"--enable-debug-command", "yes"},
	} {
		err := RefuseOwnedArgs(args)
		if err == nil {
			t.Errorf("RefuseOwnedArgs(%v) = nil, want the setting the store owns refused", args)
		}
	}
	if err := RefuseOwnedArgs([]string{"--io-threads", "4", "--maxclients", "20000"}); err != nil {
		t.Errorf("RefuseOwnedArgs() = %v, want tuning the store does not own allowed", err)
	}
}
