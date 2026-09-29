package session

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func knownHostsStore(files []string) (string, error) {
	if len(files) > 0 && files[0] != "" {
		return writable(files[0])
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return writable(filepath.Join(home, ".ssh", "known_hosts"))
}

func writable(store string) (string, error) {
	info, err := os.Stat(store)
	if errors.Is(err, os.ErrNotExist) {
		return store, nil
	}
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a regular file, so a host key recorded there would be thrown away", store)
	}
	return store, nil
}

func record(store, line string) error {
	known, err := os.ReadFile(store)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if hasLine(string(known), line) {
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(store), 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(store, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()

	if len(known) > 0 && !strings.HasSuffix(string(known), "\n") {
		line = "\n" + line
	}
	if _, err := file.WriteString(line); err != nil {
		return err
	}
	return file.Sync()
}

func hasLine(content, line string) bool {
	want := strings.Fields(line)
	if len(want) != 3 {
		return false
	}
	for _, existing := range strings.Split(content, "\n") {
		fields := strings.Fields(existing)
		if len(fields) != 3 || fields[1] != want[1] || fields[2] != want[2] {
			continue
		}
		for _, named := range strings.Split(fields[0], ",") {
			if named == want[0] {
				return true
			}
		}
	}
	return false
}
