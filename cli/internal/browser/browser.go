package browser

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/shlex"
	pkgbrowser "github.com/pkg/browser"
)

const (
	EnvVar = "BROWSER"

	stillRunningAfter = 2 * time.Second
)

func OpenURL(url string) error {
	return open(url, os.Getenv(EnvVar), pkgbrowser.OpenURL)
}

func open(url, browsers string, fallback func(url string) error) error {
	var failed []error
	for _, browser := range filepath.SplitList(browsers) {
		switch browser = strings.TrimSpace(browser); browser {
		case "":
			continue
		case "none":
			return nil
		}
		err := run(browser, url)
		if err == nil {
			return nil
		}
		failed = append(failed, err)
	}
	if err := fallback(url); err != nil {
		return errors.Join(append(failed, err)...)
	}
	return nil
}

func run(browser, url string) error {
	words, err := shlex.Split(browser)
	if err != nil || len(words) == 0 {
		return fmt.Errorf("$%s names %q, which is no command: %w", EnvVar, browser, err)
	}
	placed := false
	args := make([]string, 0, len(words))
	for _, word := range words[1:] {
		placed = placed || strings.Contains(strings.ReplaceAll(word, "%%", ""), "%s")
		args = append(args, strings.NewReplacer("%%", "%", "%s", url).Replace(word))
	}
	if !placed {
		args = append(args, url)
	}
	cmd := exec.Command(words[0], args...)
	cmd.Stdout, cmd.Stderr = pkgbrowser.Stdout, pkgbrowser.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("$%s names %s: %w", EnvVar, words[0], err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	select {
	case err := <-exited:
		if err != nil {
			return fmt.Errorf("$%s names %s, which failed: %w", EnvVar, words[0], err)
		}
		return nil
	case <-time.After(stillRunningAfter):
		return nil
	}
}
