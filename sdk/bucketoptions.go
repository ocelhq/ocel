package ocel

import (
	"maps"
	"time"
)

// A WriteOption describes or conditions the object a write leaves behind.
type WriteOption interface {
	applyWrite(*writeOptions)
}

// A ListOption bounds the keys a listing walks.
type ListOption interface {
	applyList(*listOptions)
}

// A SignOption bounds what the target a signature hands out may be used for.
type SignOption interface {
	applySign(*signOptions)
}

// A WriteSignOption bounds both a write this app makes and a target it signs
// for someone else to make.
type WriteSignOption interface {
	WriteOption
	SignOption
}

type writeOptions struct {
	contentType  string
	cacheControl string
	metadata     map[string]string
	ifNoneMatch  string
	ifMatch      string
}

type listOptions struct {
	prefix string
	limit  int32
}

type signOptions struct {
	contentType string
	maxSize     int64
	download    string
	expires     time.Duration
}

type contentTypeOption string

func (o contentTypeOption) applyWrite(w *writeOptions) { w.contentType = string(o) }
func (o contentTypeOption) applySign(s *signOptions)   { s.contentType = string(o) }

// ContentType is the media type an object is written under, and the only media
// type a signed upload target accepts.
func ContentType(mediaType string) WriteSignOption { return contentTypeOption(mediaType) }

type cacheControlOption string

func (o cacheControlOption) applyWrite(w *writeOptions) { w.cacheControl = string(o) }

// CacheControl is the cache-control the store serves the object with.
func CacheControl(value string) WriteOption { return cacheControlOption(value) }

type metadataOption map[string]string

func (o metadataOption) applyWrite(w *writeOptions) { w.metadata = maps.Clone(o) }

// Metadata is the user metadata kept beside the object, capped at 2 KB.
func Metadata(entries map[string]string) WriteOption { return metadataOption(entries) }

type ifNotExistsOption struct{}

func (ifNotExistsOption) applyWrite(w *writeOptions) { w.ifNoneMatch = "*" }

// IfNotExists writes only when the bucket has no object under the key, and
// refuses with [ErrPreconditionFailed] when it does.
func IfNotExists() WriteOption { return ifNotExistsOption{} }

type ifMatchOption string

func (o ifMatchOption) applyWrite(w *writeOptions) { w.ifMatch = string(o) }

// IfMatch writes only when the object under the key still has this etag,
// and refuses with [ErrPreconditionFailed] when it does not.
func IfMatch(etag string) WriteOption { return ifMatchOption(etag) }

type prefixOption string

func (o prefixOption) applyList(l *listOptions) { l.prefix = string(o) }

// Prefix walks only the keys that start with it.
func Prefix(prefix string) ListOption { return prefixOption(prefix) }

type limitOption int32

func (o limitOption) applyList(l *listOptions) { l.limit = int32(o) }

// Limit is how many objects one page of a listing contains, at most 1000. The
// listing itself is not bounded by it.
func Limit(objects int) ListOption { return limitOption(objects) }

type expiresOption time.Duration

func (o expiresOption) applySign(s *signOptions) { s.expires = time.Duration(o) }

// Expires is how long a signed url or upload target stays valid. Left out, the
// runtime picks the lifetime.
func Expires(in time.Duration) SignOption { return expiresOption(in) }

type maxSizeOption int64

func (o maxSizeOption) applySign(s *signOptions) { s.maxSize = int64(o) }

// MaxSize is the largest body a signed upload target accepts, in bytes.
func MaxSize(bytes int64) SignOption { return maxSizeOption(bytes) }

type downloadOption string

func (o downloadOption) applySign(s *signOptions) { s.download = string(o) }

// Download serves what a signed url reads as an attachment under this filename.
func Download(filename string) SignOption { return downloadOption(filename) }
