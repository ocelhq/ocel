package vps

import (
	"context"
	"encoding/base64"
	"fmt"
	"path"
	"strings"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

const recordsRoot = deployHome + "/records"

type hostRecords struct{ machine *host }

func (r hostRecords) pathOf(name providerkit.RecordName) string {
	return path.Join(append([]string{recordsRoot}, name...)...) + ".rec"
}

func (r hostRecords) asDeploy(command string) string {
	return r.machine.sudo(fmt.Sprintf("-u %s sh -c %s", deployUser, singleQuote(command)))
}

func (r hostRecords) Read(ctx context.Context, name providerkit.RecordName) (providerkit.Record, error) {
	if err := r.machine.probe(ctx); err != nil {
		return providerkit.Record{}, err
	}
	at := r.pathOf(name)
	got, err := r.machine.check(ctx, r.asDeploy(shellQuote("base64", "-w0", at))+" 2>/dev/null || true")
	if err != nil {
		return providerkit.Record{}, err
	}
	if strings.TrimSpace(got) == "" {
		return providerkit.Record{}, providerkit.ErrNoRecord
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(got))
	if err != nil {
		return providerkit.Record{}, err
	}
	return providerkit.Record{Name: name, Bytes: raw, Revision: providerkit.Revision(sha256hex(string(raw)))}, nil
}

func (r hostRecords) Write(ctx context.Context, record providerkit.Record) (providerkit.Revision, error) {
	if err := r.machine.probe(ctx); err != nil {
		return "", err
	}
	at := r.pathOf(record.Name)
	encoded := base64.StdEncoding.EncodeToString(record.Bytes)
	command := fmt.Sprintf("mkdir -p %s && base64 -d > %s.tmp && mv %s.tmp %s",
		shellQuote(path.Dir(at)), shellQuote(at), shellQuote(at), shellQuote(at))
	if _, errOut, err := r.machine.run(ctx, []byte(encoded), r.asDeploy(command)); err != nil {
		return "", providerkit.Refuse(providerkit.CodeNotReady, "writing record %s: %v: %s", at, err, strings.TrimSpace(errOut))
	}
	return providerkit.Revision(sha256hex(string(record.Bytes))), nil
}

func (r hostRecords) WritePair(ctx context.Context, first, second providerkit.Record) error {
	if _, err := r.Write(ctx, first); err != nil {
		return err
	}
	_, err := r.Write(ctx, second)
	return err
}

func (r hostRecords) Remove(ctx context.Context, name providerkit.RecordName, expected providerkit.Revision) error {
	current, err := r.Read(ctx, name)
	if err != nil {
		return err
	}
	if expected != "" && current.Revision != expected {
		return providerkit.ErrStale
	}
	return r.machine.must(ctx, r.asDeploy(shellQuote("rm", "-f", r.pathOf(name))))
}

func (r hostRecords) List(ctx context.Context, under providerkit.RecordName) ([]providerkit.Record, error) {
	if err := r.machine.probe(ctx); err != nil {
		return nil, err
	}
	root := path.Join(append([]string{recordsRoot}, under...)...)
	listing, err := r.machine.check(ctx, r.asDeploy(shellQuote("find", root, "-name", "*.rec"))+" 2>/dev/null || true")
	if err != nil {
		return nil, err
	}
	var records []providerkit.Record
	for _, line := range strings.Fields(listing) {
		trimmed := strings.TrimSuffix(strings.TrimPrefix(line, recordsRoot+"/"), ".rec")
		name := providerkit.RecordName(strings.Split(trimmed, "/"))
		record, err := r.Read(ctx, name)
		if err != nil {
			continue
		}
		records = append(records, record)
	}
	return records, nil
}

func singleQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

var _ providerkit.RecordStore = hostRecords{}
