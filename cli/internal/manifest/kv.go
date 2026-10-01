package manifest

import (
	"cmp"
	"fmt"

	"github.com/ocelhq/ocel/cli/internal/attribution"
	"github.com/ocelhq/ocel/pkg/kvstore"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
)

type declaredEntry struct {
	Name    string
	Pattern string
	Shape   resourcesv1.KvShape
	Source  string
}

func declaredEntries(configDir string, config *resourcesv1.KvConfig, storeSource string) []declaredEntry {
	entries := make([]declaredEntry, 0, len(config.GetEntries()))
	for _, entry := range config.GetEntries() {
		source := storeSource
		if site, ok := attribution.DeclaringSite(configDir, entry.GetSource()); ok {
			source = site.String()
		}
		entries = append(entries, declaredEntry{Name: entry.GetName(), Pattern: entry.GetPattern(), Shape: entry.GetShape(), Source: source})
	}
	return entries
}

func refuseKVStore(d declaredResource) error {
	config := d.KV
	if version := config.GetVersion(); version != "" {
		if err := kvstore.RefuseVersion(version); err != nil {
			return refuse(d, "%s", err)
		}
	}
	if eviction := config.GetEviction(); eviction != "" {
		if err := kvstore.RefuseEviction(eviction); err != nil {
			return refuse(d, "%s", err)
		}
	}
	if memory := config.GetMemory(); memory != "" {
		if _, err := kvstore.ParseMemory(memory); err != nil {
			return refuse(d, "%s", err)
		}
	}
	return refuseKVEntries(d)
}

func refuseKVEntries(d declaredResource) error {
	type parsedEntry struct {
		entry   declaredEntry
		pattern kvstore.Pattern
	}
	parsed := make([]parsedEntry, 0, len(d.KVEntries))
	for _, entry := range d.KVEntries {
		if err := kvstore.RefuseEntryName(entry.Name); err != nil {
			return refuseEntry(d, entry, "%s", err)
		}
		if entry.Shape == resourcesv1.KvShape_KV_SHAPE_UNSPECIFIED {
			return refuseEntry(d, entry, "declares no shape: it is text, counter, json, list or set")
		}
		pattern, err := kvstore.ParsePattern(entry.Pattern)
		if err != nil {
			return refuseEntry(d, entry, "%s", err)
		}
		for _, prior := range parsed {
			if prior.entry.Name == entry.Name {
				return refuseEntry(d, entry, "is declared already at %s, and a store names each entry once", sourceOrUnknown(prior.entry.Source))
			}
			if prior.pattern.Overlaps(pattern) {
				return refuseEntry(d, entry,
					"has pattern %q, which overlaps pattern %q of entry %q declared at %s: some key would match both, so neither entry could tell its keys from the other's",
					entry.Pattern, prior.entry.Pattern, prior.entry.Name, sourceOrUnknown(prior.entry.Source))
			}
		}
		parsed = append(parsed, parsedEntry{entry, pattern})
	}
	return nil
}

func refuseEntry(d declaredResource, entry declaredEntry, format string, args ...any) error {
	return &InvalidDeclarationError{
		Source: cmp.Or(entry.Source, d.Source),
		Reason: fmt.Sprintf("entry %q of %s ", entry.Name, d.label()) + fmt.Sprintf(format, args...),
	}
}

func manifestKV(d declaredResource) *resourcesv1.KvConfig {
	entries := make([]*resourcesv1.KvEntry, 0, len(d.KVEntries))
	for _, entry := range d.KVEntries {
		entries = append(entries, &resourcesv1.KvEntry{Name: entry.Name, Pattern: entry.Pattern, Shape: entry.Shape, Source: entry.Source})
	}
	return &resourcesv1.KvConfig{
		Version:  d.KV.GetVersion(),
		Eviction: d.KV.GetEviction(),
		Memory:   d.KV.GetMemory(),
		Entries:  entries,
	}
}
