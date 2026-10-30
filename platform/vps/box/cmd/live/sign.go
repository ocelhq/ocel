package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/ocelhq/ocel/platform/vps/box/usercertificate"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
)

const (
	signNamed        = "ocel-live " + boxapi.SignCommand
	signRequestBytes = 16 << 10
)

type authorityFiles struct {
	key    string
	config string
}

func signCommand(argv []string, stdin io.Reader, stdout, errs io.Writer) int {
	return signCertificate(authorityFiles{key: boxapi.AuthorityKey, config: boxapi.AuthorityConfigPath}, argv, stdin, stdout, errs, time.Now())
}

func signCertificate(files authorityFiles, argv []string, stdin io.Reader, stdout, errs io.Writer, now time.Time) int {
	signed, err := sign(files, argv, stdin, now)
	if err != nil {
		fmt.Fprintf(errs, "%s: %s\n", signNamed, err)
		return boxapi.ExitRefused
	}
	fmt.Fprintln(stdout, signed)
	return 0
}

func sign(files authorityFiles, argv []string, stdin io.Reader, now time.Time) (string, error) {
	if len(argv) != 0 {
		return "", fmt.Errorf("takes no arguments: the request is read from stdin")
	}
	raw, err := io.ReadAll(io.LimitReader(stdin, signRequestBytes+1))
	if err != nil {
		return "", err
	}
	if len(raw) > signRequestBytes {
		return "", fmt.Errorf("the request is over %d bytes", signRequestBytes)
	}
	var request boxapi.CertificateRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		return "", fmt.Errorf("read the request: %w", err)
	}
	read, err := os.ReadFile(files.config)
	if err != nil {
		return "", err
	}
	var config boxapi.AuthorityConfig
	if err := json.Unmarshal(read, &config); err != nil {
		return "", fmt.Errorf("read %s: %w", files.config, err)
	}
	key, err := os.ReadFile(files.key)
	if err != nil {
		return "", err
	}
	return usercertificate.Sign(key, config.Principal, request, now)
}
