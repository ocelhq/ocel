package boxapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/refusal"
)

const (
	EntrySuffix      = ".json"
	segmentSeparator = "+"
	stagedSuffix     = ".staged"
	lockFile         = ".lock"
	entryFileMode    = 0o600
	entryDirMode     = 0o700
	changesPerSet    = 2
)

func KeyValuesDir(root string, tier environment.Tier) string {
	return filepath.Join(root, string(tier), "keyvalues")
}

type KeyValues struct{ Root string }

type EntryFile struct {
	Revision records.Revision `json:"revision"`
	Value    json.RawMessage  `json:"value"`
}

type ListedEntry struct {
	Path     string           `json:"path"`
	Revision records.Revision `json:"revision"`
	Value    json.RawMessage  `json:"value"`
}

type Change struct {
	Path     string           `json:"path"`
	Revision records.Revision `json:"revision"`
	Value    json.RawMessage  `json:"value,omitempty"`
	Remove   bool             `json:"remove,omitempty"`
}

func (s KeyValues) Read(ctx context.Context, key records.Key) (records.Entry, error) {
	path, err := PathOf(key)
	if err != nil {
		return records.Entry{}, err
	}
	file, err := s.ReadPath(ctx, key.Partition.Tier, path)
	if err != nil {
		return records.Entry{}, err
	}
	return records.Entry{Key: key, Value: file.Value, Revision: file.Revision}, nil
}

func (s KeyValues) List(ctx context.Context, in records.Partition, under ...string) ([]records.Entry, error) {
	dir, err := PartitionDir(in)
	if err != nil {
		return nil, err
	}
	prefix := ""
	if len(under) > 0 {
		if prefix, err = encodePath(under); err != nil {
			return nil, err
		}
	}
	listed, err := s.ListPath(ctx, in.Tier, dir, prefix)
	if err != nil {
		return nil, err
	}
	found := make([]records.Entry, 0, len(listed))
	for _, entry := range listed {
		key, err := KeyOf(in, entry.Path)
		if err != nil {
			return nil, err
		}
		found = append(found, records.Entry{Key: key, Value: entry.Value, Revision: entry.Revision})
	}
	return found, nil
}

func (s KeyValues) Write(ctx context.Context, entry records.Entry) (records.Revision, error) {
	if err := records.RefuseUnwritable(entry); err != nil {
		return "", err
	}
	path, err := PathOf(entry.Key)
	if err != nil {
		return "", err
	}
	revisions, err := s.CompareAndSet(ctx, entry.Key.Partition.Tier, []Change{{Path: path, Revision: entry.Revision, Value: entry.Value}})
	if err != nil {
		return "", err
	}
	return revisions[0], nil
}

func (s KeyValues) WritePair(ctx context.Context, first, second records.Entry) error {
	changes := make([]Change, 0, changesPerSet)
	for _, entry := range []records.Entry{first, second} {
		if err := records.RefuseUnwritable(entry); err != nil {
			return err
		}
		path, err := PathOf(entry.Key)
		if err != nil {
			return err
		}
		changes = append(changes, Change{Path: path, Revision: entry.Revision, Value: entry.Value})
	}
	if first.Key.Partition.Tier != second.Key.Partition.Tier {
		return refusal.Refuse(refusal.CodeInvalid, "%s and %s belong to different tiers", first.Key, second.Key)
	}
	_, err := s.CompareAndSet(ctx, first.Key.Partition.Tier, changes)
	return err
}

func (s KeyValues) Remove(ctx context.Context, key records.Key, expected records.Revision) error {
	path, err := PathOf(key)
	if err != nil {
		return err
	}
	_, err = s.CompareAndSet(ctx, key.Partition.Tier, []Change{{Path: path, Revision: expected, Remove: true}})
	return err
}

func (s KeyValues) ReadPath(_ context.Context, tier environment.Tier, path string) (EntryFile, error) {
	if err := refuseOutside(path); err != nil {
		return EntryFile{}, err
	}
	dir, found, err := s.findStoreDir(tier)
	if err != nil {
		return EntryFile{}, err
	}
	if !found {
		return EntryFile{}, records.ErrNotFound
	}
	unlock, err := lockShared(dir)
	if err != nil {
		return EntryFile{}, err
	}
	defer unlock()
	file, found, err := readEntryFile(dir, path+EntrySuffix)
	if err != nil {
		return EntryFile{}, err
	}
	if !found {
		return EntryFile{}, records.ErrNotFound
	}
	return file, nil
}

func (s KeyValues) ListPath(_ context.Context, tier environment.Tier, partition, under string) ([]ListedEntry, error) {
	if err := refuseOutside(partition); err != nil {
		return nil, err
	}
	beneath := partition
	if under != "" {
		if err := refuseOutside(under); err != nil {
			return nil, err
		}
		beneath = partition + "/" + under
	}
	dir, found, err := s.findStoreDir(tier)
	if err != nil || !found {
		return nil, err
	}
	unlock, err := lockShared(dir)
	if err != nil {
		return nil, err
	}
	defer unlock()
	var listed []ListedEntry
	if under != "" {
		file, found, err := readEntryFile(dir, beneath+EntrySuffix)
		if err != nil {
			return nil, err
		}
		if found {
			listed = append(listed, ListedEntry{Path: under, Revision: file.Revision, Value: file.Value})
		}
	}
	if _, found, err := lstatInside(dir, beneath); err != nil || !found {
		return listed, err
	}
	err = filepath.WalkDir(filepath.Join(dir, beneath), func(path string, visited fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !visited.Type().IsRegular() || !strings.HasSuffix(visited.Name(), EntrySuffix) {
			return nil
		}
		relative, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		file, _, err := readEntryFile(dir, filepath.ToSlash(relative))
		if err != nil {
			return err
		}
		entry := strings.TrimPrefix(strings.TrimSuffix(filepath.ToSlash(relative), EntrySuffix), partition+"/")
		listed = append(listed, ListedEntry{Path: entry, Revision: file.Revision, Value: file.Value})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return listed, nil
}

func (s KeyValues) CompareAndSet(_ context.Context, tier environment.Tier, changes []Change) ([]records.Revision, error) {
	lines, err := compactChanges(changes)
	if err != nil {
		return nil, err
	}
	dir, found, err := s.findStoreDir(tier)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, refusal.Refuse(refusal.CodeNotReady, "%s is missing\nRun `%s`", dir, provider.BootstrapCommand(tier))
	}
	owner, err := ownerOf(dir)
	if err != nil {
		return nil, err
	}
	unlock, err := lockExclusive(dir, owner)
	if err != nil {
		return nil, err
	}
	defer unlock()
	for _, change := range changes {
		current, found, err := readEntryFile(dir, change.Path+EntrySuffix)
		if err != nil {
			return nil, err
		}
		if change.Remove && !found {
			return nil, records.ErrNotFound
		}
		if current.Revision != change.Revision {
			return nil, records.ErrStale
		}
	}
	revisions := make([]records.Revision, len(changes))
	staged := map[int]string{}
	defer func() {
		for _, path := range staged {
			_ = os.Remove(path)
		}
	}()
	for i, change := range changes {
		if change.Remove {
			continue
		}
		if revisions[i], err = records.NewRevision(); err != nil {
			return nil, err
		}
		if staged[i], err = stage(dir, change.Path+EntrySuffix, revisions[i], lines[i], owner); err != nil {
			return nil, err
		}
	}
	for i, change := range changes {
		if change.Remove {
			continue
		}
		if err := os.Rename(staged[i], filepath.Join(dir, change.Path+EntrySuffix)); err != nil {
			return nil, err
		}
		delete(staged, i)
	}
	for _, change := range changes {
		if !change.Remove {
			continue
		}
		file := filepath.Join(dir, change.Path+EntrySuffix)
		if err := os.Remove(file); err != nil {
			return nil, err
		}
		prune(dir, file)
	}
	return revisions, nil
}

func compactChanges(changes []Change) ([][]byte, error) {
	if len(changes) == 0 || len(changes) > changesPerSet {
		return nil, refusal.Refuse(refusal.CodeInvalid, "a change set holds %d changes, want 1 or %d", len(changes), changesPerSet)
	}
	lines := make([][]byte, len(changes))
	named := map[string]bool{}
	for i, change := range changes {
		if err := refuseOutside(change.Path); err != nil {
			return nil, err
		}
		if named[change.Path] {
			return nil, refusal.Refuse(refusal.CodeInvalid, "%s is changed twice in one change set", change.Path)
		}
		named[change.Path] = true
		if change.Remove {
			if len(change.Value) != 0 {
				return nil, refusal.Refuse(refusal.CodeInvalid, "the removal of %s carries a value", change.Path)
			}
			continue
		}
		if len(change.Value) == 0 {
			return nil, refusal.Refuse(refusal.CodeInvalid, "the value of %s is empty", change.Path)
		}
		var line bytes.Buffer
		if err := json.Compact(&line, change.Value); err != nil {
			return nil, refusal.Refuse(refusal.CodeInvalid, "the value of %s is not JSON, and every entry is kept as JSON text", change.Path)
		}
		lines[i] = line.Bytes()
	}
	return lines, nil
}

func refuseOutside(path string) error {
	for _, segment := range strings.Split(path, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return refusal.Refuse(refusal.CodeInvalid, "%q is not a path inside the tier's records", path)
		}
	}
	return nil
}

func (s KeyValues) findStoreDir(tier environment.Tier) (string, bool, error) {
	dir := KeyValuesDir(s.Root, tier)
	info, err := os.Lstat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return dir, false, nil
	case errors.Is(err, fs.ErrPermission):
		return dir, false, refuseUnreachable(dir, err)
	case err != nil:
		return dir, false, err
	case info.Mode()&fs.ModeSymlink != 0:
		target, _ := os.Readlink(dir)
		return dir, false, refusal.Refuse(refusal.CodeDenied, "%s is a symlink to %s, not the directory ocel bootstrap made", dir, target)
	case !info.IsDir():
		return dir, false, refusal.Refuse(refusal.CodeDenied, "%s is not the directory ocel bootstrap made", dir)
	}
	return dir, true, nil
}

func refuseUnreachable(dir string, err error) error {
	parts := strings.Split(filepath.Dir(dir), string(filepath.Separator))
	for i := range parts {
		ancestor := strings.Join(parts[:i+1], string(filepath.Separator))
		if ancestor == "" {
			continue
		}
		if info, statErr := os.Stat(ancestor); statErr == nil && info.IsDir() && unix.Faccessat(unix.AT_FDCWD, ancestor, unix.X_OK, unix.AT_EACCESS) != nil {
			return refusal.Refuse(refusal.CodeDenied, "%s: Permission denied to %s", ancestor, currentUser())
		}
	}
	return refusal.Refuse(refusal.CodeDenied, "%s: %s", dir, err)
}

func currentUser() string {
	if current, err := user.Current(); err == nil {
		return current.Username
	}
	return strconv.Itoa(os.Geteuid())
}

func lstatInside(dir, path string) (fs.FileInfo, bool, error) {
	at := dir
	var info fs.FileInfo
	for _, segment := range strings.Split(path, "/") {
		at = filepath.Join(at, segment)
		var err error
		info, err = os.Lstat(at)
		if errors.Is(err, fs.ErrNotExist) {
			return nil, false, nil
		}
		if err != nil {
			return nil, false, err
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return nil, false, refusal.Refuse(refusal.CodeDenied, "%s is a symlink", at)
		}
	}
	return info, true, nil
}

func readEntryFile(dir, path string) (EntryFile, bool, error) {
	info, found, err := lstatInside(dir, path)
	if err != nil || !found {
		return EntryFile{}, false, err
	}
	at := filepath.Join(dir, path)
	if !info.Mode().IsRegular() {
		return EntryFile{}, false, refusal.Refuse(refusal.CodeDenied, "%s is not an entry ocel wrote", at)
	}
	opened, err := os.OpenFile(at, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return EntryFile{}, false, err
	}
	defer opened.Close()
	var file EntryFile
	if err := json.NewDecoder(opened).Decode(&file); err != nil || file.Revision == "" || len(file.Value) == 0 {
		return EntryFile{}, false, refusal.Refuse(refusal.CodeDenied, "%s is not an entry ocel wrote", at)
	}
	return file, true, nil
}

func stage(dir, path string, revision records.Revision, value []byte, owner *fileOwner) (string, error) {
	file := filepath.Join(dir, path)
	if err := ensureParents(dir, path); err != nil {
		return "", err
	}
	staged := file + stagedSuffix
	if _, _, err := lstatInside(dir, path+stagedSuffix); err != nil {
		return "", err
	}
	opened, err := os.OpenFile(staged, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|syscall.O_NOFOLLOW, entryFileMode)
	if err != nil {
		return "", err
	}
	_, err = fmt.Fprintf(opened, `{"revision":"%s","value":%s}`+"\n", revision, value)
	err = errors.Join(err, opened.Sync(), opened.Close())
	if err == nil {
		err = owner.handOver(dir, staged)
	}
	if err != nil {
		_ = os.Remove(staged)
		return "", err
	}
	return staged, nil
}

func ensureParents(dir, path string) error {
	segments := strings.Split(path, "/")
	at := dir
	for _, segment := range segments[:len(segments)-1] {
		at = filepath.Join(at, segment)
		info, err := os.Lstat(at)
		if errors.Is(err, fs.ErrNotExist) {
			if err := os.Mkdir(at, entryDirMode); err != nil && !errors.Is(err, fs.ErrExist) {
				return err
			}
			info, err = os.Lstat(at)
		}
		if err != nil {
			return err
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return refusal.Refuse(refusal.CodeDenied, "%s is a symlink", at)
		}
		if !info.IsDir() {
			return refusal.Refuse(refusal.CodeDenied, "%s is not a directory ocel made", at)
		}
	}
	return nil
}

func prune(dir, file string) {
	for at := filepath.Dir(file); at != dir && strings.HasPrefix(at, dir+string(filepath.Separator)); at = filepath.Dir(at) {
		names, _ := os.ReadDir(at)
		for _, name := range names {
			if strings.HasSuffix(name.Name(), EntrySuffix+stagedSuffix) {
				_ = os.Remove(filepath.Join(at, name.Name()))
			}
		}
		if os.Remove(at) != nil {
			return
		}
	}
}

type fileOwner struct{ uid, gid int }

func ownerOf(dir string) (*fileOwner, error) {
	if os.Geteuid() != 0 {
		return nil, nil
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil, fmt.Errorf("read the owner of %s", dir)
	}
	return &fileOwner{uid: int(stat.Uid), gid: int(stat.Gid)}, nil
}

func (o *fileOwner) handOver(dir, path string) error {
	if o == nil {
		return nil
	}
	for at := path; at != dir && strings.HasPrefix(at, dir+string(filepath.Separator)); at = filepath.Dir(at) {
		if err := os.Lchown(at, o.uid, o.gid); err != nil {
			return refusal.Refuse(refusal.CodeDenied, "could not hand %s to the owner of %s: %s", at, dir, err)
		}
	}
	return nil
}

func lockExclusive(dir string, owner *fileOwner) (func(), error) {
	path := filepath.Join(dir, lockFile)
	if _, _, err := lstatInside(dir, lockFile); err != nil {
		return nil, err
	}
	opened, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND|syscall.O_NOFOLLOW, entryFileMode)
	if err != nil {
		return nil, err
	}
	if err := owner.handOver(dir, path); err != nil {
		opened.Close()
		return nil, err
	}
	return flock(opened, unix.LOCK_EX)
}

func lockShared(dir string) (func(), error) {
	if _, _, err := lstatInside(dir, lockFile); err != nil {
		return nil, err
	}
	opened, err := os.OpenFile(filepath.Join(dir, lockFile), os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if errors.Is(err, fs.ErrNotExist) {
		return func() {}, nil
	}
	if err != nil {
		return nil, err
	}
	return flock(opened, unix.LOCK_SH)
}

func flock(opened *os.File, how int) (func(), error) {
	for {
		err := unix.Flock(int(opened.Fd()), how)
		if err == nil {
			return func() { opened.Close() }, nil
		}
		if !errors.Is(err, unix.EINTR) {
			opened.Close()
			return nil, fmt.Errorf("lock %s: %w", opened.Name(), err)
		}
	}
}

func PartitionDir(in records.Partition) (string, error) {
	if err := records.RefuseMalformedPartition(in); err != nil {
		return "", err
	}
	segments := in.Segments()
	for i, segment := range segments {
		segments[i] = encodeSegment(segment)
	}
	return strings.Join(segments, segmentSeparator), nil
}

func PathOf(key records.Key) (string, error) {
	if err := records.RefuseMalformedKey(key); err != nil {
		return "", err
	}
	dir, err := PartitionDir(key.Partition)
	if err != nil {
		return "", err
	}
	path, err := encodePath(key.Path)
	if err != nil {
		return "", err
	}
	return dir + "/" + path, nil
}

func encodePath(path []string) (string, error) {
	segments := make([]string, 0, len(path))
	for _, segment := range path {
		if segment == "" {
			return "", refusal.Refuse(refusal.CodeInvalid, "%q has an empty segment", path)
		}
		segments = append(segments, encodeSegment(segment))
	}
	return strings.Join(segments, "/"), nil
}

func encodeSegment(segment string) string {
	escaped := records.EscapeSegment(segment, isPlain)
	if strings.HasPrefix(escaped, ".") {
		return "%2E" + escaped[1:]
	}
	return escaped
}

func isPlain(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	default:
		return c == '-' || c == '_' || c == '.'
	}
}

func KeyOf(in records.Partition, encoded string) (records.Key, error) {
	var path []string
	for _, segment := range strings.Split(encoded, "/") {
		decoded, err := records.UnescapeSegment(segment)
		if err != nil {
			return records.Key{}, err
		}
		path = append(path, decoded)
	}
	return in.Key(path...), nil
}

var _ records.Store = KeyValues{}
