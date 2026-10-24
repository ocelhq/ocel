package main

import (
	"crypto/tls"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
)

const pinNamed = "ocel-live " + boxapi.PinCommand

func pinCommand(argv []string, stdout, errs io.Writer) int {
	return printPin(boxapi.PinsDir, argv, stdout, errs)
}

func printPin(dir string, argv []string, stdout, errs io.Writer) int {
	pair, err := readPin(dir, argv)
	if err == nil {
		encoder := json.NewEncoder(stdout)
		encoder.SetEscapeHTML(false)
		err = encoder.Encode(pair)
	}
	if err != nil {
		fmt.Fprintf(errs, "%s: %s\n", pinNamed, err)
		return boxapi.ExitRefused
	}
	return 0
}

func readPin(dir string, args []string) (boxapi.PinnedPair, error) {
	if len(args) != 1 {
		return boxapi.PinnedPair{}, errors.New("usage: " + pinNamed + " <name>")
	}
	if _, pinned := boxapi.Pinned(boxapi.PinsDir + "/" + args[0]); !pinned {
		return boxapi.PinnedPair{}, fmt.Errorf("%q names no certificate directly under %s", args[0], boxapi.PinsDir)
	}
	path := filepath.Join(dir, args[0])
	read, err := os.ReadFile(boxapi.PinCertificate(path))
	if err != nil {
		return boxapi.PinnedPair{}, err
	}
	certificate := certificateBlocks(read)
	key, unread := os.ReadFile(boxapi.PinKey(path))
	_, unpaired := tls.X509KeyPair(certificate, key)
	return boxapi.PinnedPair{Certificate: string(certificate), KeyMatches: unread == nil && unpaired == nil}, nil
}

func certificateBlocks(read []byte) []byte {
	var kept []byte
	for rest := read; ; {
		var block *pem.Block
		if block, rest = pem.Decode(rest); block == nil {
			return kept
		}
		if block.Type == "CERTIFICATE" {
			kept = append(kept, pem.EncodeToMemory(&pem.Block{Type: block.Type, Bytes: block.Bytes})...)
		}
	}
}
