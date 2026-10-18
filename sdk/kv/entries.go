package kv

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"ocel.dev/internal/codec"
	"ocel.dev/internal/declaration"
	resourcesv1 "ocel.dev/internal/proto/app/resources/v1"
)

type entry[K comparable] struct {
	store    *Store
	name     string
	key      keyBuilder
	settings entrySettings
}

func declareEntry[K comparable](store *Store, name, pattern string, shape resourcesv1.KvShape, opts []EntryOption) entry[K] {
	source := declaration.CallSite(1)
	refuse := func(err error) {
		panic(fmt.Sprintf("ocel: kv %q: entry %q: %v", store.name, name, err))
	}
	builder, err := newKeyBuilder(parseKeyPattern(pattern), reflect.TypeFor[K]())
	if err != nil {
		refuse(err)
	}
	var settings entrySettings
	for _, opt := range opts {
		opt.applyEntry(&settings)
	}
	if settings.ttl != 0 {
		if err := refuseShortTTL(settings.ttl); err != nil {
			refuse(err)
		}
	}
	if settings.missOnInvalid && shape != resourcesv1.KvShape_KV_SHAPE_JSON {
		refuse(errors.New("MissOnInvalid applies to a json entry alone, whose stored value can fail to decode"))
	}
	store.addEntry(&resourcesv1.KvEntry{Name: name, Pattern: pattern, Shape: shape, Source: source})
	return entry[K]{store: store, name: name, key: builder, settings: settings}
}

func (e entry[K]) open(operation string) (*redis.Client, error) {
	return e.store.open(e.name + "." + operation)
}

func (e entry[K]) keyOf(key K) string {
	return e.key.build(reflect.ValueOf(key))
}

func (e entry[K]) setString(ctx context.Context, operation string, key K, value string, opts []WriteOption) error {
	ttl, err := resolveTTL(e.settings.ttl, opts)
	if err != nil {
		return err
	}
	client, err := e.open(operation)
	if err != nil {
		return err
	}
	expiration := time.Duration(0)
	switch ttl.mode {
	case ttlExpire:
		expiration = ttl.after
	case ttlKeep:
		expiration = redis.KeepTTL
	}
	return client.Set(ctx, e.keyOf(key), value, expiration).Err()
}

func (e entry[K]) write(ctx context.Context, operation string, key K, opts []WriteOption, command func(redis.Pipeliner, string) *redis.IntCmd) (int64, error) {
	ttl, err := resolveTTL(e.settings.ttl, opts)
	if err != nil {
		return 0, err
	}
	client, err := e.open(operation)
	if err != nil {
		return 0, err
	}
	built := e.keyOf(key)
	var result *redis.IntCmd
	_, err = client.TxPipelined(ctx, func(transaction redis.Pipeliner) error {
		result = command(transaction, built)
		switch ttl.mode {
		case ttlExpire:
			transaction.PExpire(ctx, built, ttl.after)
		case ttlClear:
			transaction.Persist(ctx, built)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return result.Val(), nil
}

func (e entry[K]) getString(ctx context.Context, operation string, key K) (string, string, error) {
	client, err := e.open(operation)
	if err != nil {
		return "", "", err
	}
	built := e.keyOf(key)
	value, err := client.Get(ctx, built).Result()
	if errors.Is(err, redis.Nil) {
		return built, "", ErrMiss
	}
	return built, value, err
}

func (e entry[K]) getStrings(ctx context.Context, keys []K) (map[K]string, error) {
	client, err := e.open("GetMany")
	if err != nil {
		return nil, err
	}
	commands := make([]*redis.StringCmd, len(keys))
	_, err = client.Pipelined(ctx, func(pipeline redis.Pipeliner) error {
		for i, key := range keys {
			commands[i] = pipeline.Get(ctx, e.keyOf(key))
		}
		return nil
	})
	if err != nil && !errors.Is(err, redis.Nil) {
		return nil, err
	}
	values := make(map[K]string, len(keys))
	for i, command := range commands {
		value, err := command.Result()
		if errors.Is(err, redis.Nil) {
			continue
		}
		if err != nil {
			return nil, err
		}
		values[keys[i]] = value
	}
	return values, nil
}

// Delete deletes the key, reporting whether it held a value.
func (e entry[K]) Delete(ctx context.Context, key K) (bool, error) {
	client, err := e.open("Delete")
	if err != nil {
		return false, err
	}
	deleted, err := client.Del(ctx, e.keyOf(key)).Result()
	return deleted > 0, err
}

// A Text is a text entry of a [Store]: a string under each key.
type Text[K comparable] struct{ entry[K] }

// NewText declares a text entry named name on store, whose keys are built from pattern:
// /-separated segments that are each a literal or a :parameter. K is a string or an
// integer for a pattern of one parameter, struct{} for a pattern of none, and otherwise a
// struct whose fields are the parameters, matched by name regardless of case or by a
// `kv:"name"` tag. name starts with an ASCII letter and goes on in ASCII letters, digits
// and _, at most 63 of them, and is none of client, connectionString,
// connection_string, then and constructor: the deploy refuses a name otherwise, a
// malformed pattern and a pattern that overlaps another entry's. A K that does not
// match the pattern panics here.
func NewText[K comparable](store *Store, name, pattern string, opts ...EntryOption) *Text[K] {
	return &Text[K]{declareEntry[K](store, name, pattern, resourcesv1.KvShape_KV_SHAPE_TEXT, opts)}
}

// Get is the string under the key, or [ErrMiss] when there is none.
func (e *Text[K]) Get(ctx context.Context, key K) (string, error) {
	_, value, err := e.getString(ctx, "Get", key)
	return value, err
}

// GetMany is the string under each key that holds one, read in one round trip.
func (e *Text[K]) GetMany(ctx context.Context, keys ...K) (map[K]string, error) {
	return e.getStrings(ctx, keys)
}

// Set writes value under the key, with the entry's TTL unless an option says otherwise.
func (e *Text[K]) Set(ctx context.Context, key K, value string, opts ...WriteOption) error {
	return e.setString(ctx, "Set", key, value, opts)
}

// A Counter is a counter entry of a [Store]: an integer under each key, changed
// atomically.
type Counter[K comparable] struct{ entry[K] }

// NewCounter declares a counter entry named name on store. Its name, pattern and K are
// written as [NewText]'s are.
func NewCounter[K comparable](store *Store, name, pattern string, opts ...EntryOption) *Counter[K] {
	return &Counter[K]{declareEntry[K](store, name, pattern, resourcesv1.KvShape_KV_SHAPE_COUNTER, opts)}
}

func decodeCounter(key, value string) (int64, error) {
	count, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, &InvalidValueError{Key: key, Reason: fmt.Sprintf("holds %q, which is no integer", value)}
	}
	return count, nil
}

// Get is the integer under the key, or [ErrMiss] when there is none.
func (e *Counter[K]) Get(ctx context.Context, key K) (int64, error) {
	built, value, err := e.getString(ctx, "Get", key)
	if err != nil {
		return 0, err
	}
	return decodeCounter(built, value)
}

// GetMany is the integer under each key that holds one, read in one round trip.
func (e *Counter[K]) GetMany(ctx context.Context, keys ...K) (map[K]int64, error) {
	values, err := e.getStrings(ctx, keys)
	if err != nil {
		return nil, err
	}
	counts := make(map[K]int64, len(values))
	for key, value := range values {
		if counts[key], err = decodeCounter(e.keyOf(key), value); err != nil {
			return nil, err
		}
	}
	return counts, nil
}

// Set writes value under the key, with the entry's TTL unless an option says otherwise.
func (e *Counter[K]) Set(ctx context.Context, key K, value int64, opts ...WriteOption) error {
	return e.setString(ctx, "Set", key, strconv.FormatInt(value, 10), opts)
}

// Increment adds by to the integer under the key, from 0 when there is none, and returns
// the sum. The entry's TTL is applied in the same transaction unless an option says
// otherwise.
func (e *Counter[K]) Increment(ctx context.Context, key K, by int64, opts ...WriteOption) (int64, error) {
	return e.write(ctx, "Increment", key, opts, func(transaction redis.Pipeliner, built string) *redis.IntCmd {
		return transaction.IncrBy(ctx, built, by)
	})
}

// Decrement subtracts by from the integer under the key, from 0 when there is none, and
// returns the difference, as [Counter.Increment] does.
func (e *Counter[K]) Decrement(ctx context.Context, key K, by int64, opts ...WriteOption) (int64, error) {
	return e.write(ctx, "Decrement", key, opts, func(transaction redis.Pipeliner, built string) *redis.IntCmd {
		return transaction.DecrBy(ctx, built, by)
	})
}

// A JSON is a json entry of a [Store]: a V under each key, stored as JSON.
type JSON[K comparable, V any] struct{ entry[K] }

// NewJSON declares a json entry named name on store, whose values are V encoded as JSON;
// V is its schema. Its name, pattern and K are written as [NewText]'s are. A stored
// value that does not decode into V, that is null, that holds a field V lacks, or that
// has more after it, is an [InvalidValueError] on read, or a miss under
// [MissOnInvalid].
func NewJSON[K comparable, V any](store *Store, name, pattern string, opts ...EntryOption) *JSON[K, V] {
	return &JSON[K, V]{declareEntry[K](store, name, pattern, resourcesv1.KvShape_KV_SHAPE_JSON, opts)}
}

func decodeStoredJSON(raw string, into any) error {
	if strings.TrimSpace(raw) == "null" {
		return errors.New("null is no value of it")
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(into); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("more follows the value")
	}
	return nil
}

func (e *JSON[K, V]) decode(key, raw string) (V, error) {
	var value V
	if err := decodeStoredJSON(raw, &value); err != nil {
		if e.settings.missOnInvalid {
			return value, ErrMiss
		}
		return value, &InvalidValueError{Key: key, Reason: fmt.Sprintf("holds a value that does not decode into a %T: %v", value, err)}
	}
	return value, nil
}

// Get is the value under the key, or [ErrMiss] when there is none.
func (e *JSON[K, V]) Get(ctx context.Context, key K) (V, error) {
	built, raw, err := e.getString(ctx, "Get", key)
	if err != nil {
		var zero V
		return zero, err
	}
	return e.decode(built, raw)
}

// GetMany is the value under each key that holds one, read in one round trip.
func (e *JSON[K, V]) GetMany(ctx context.Context, keys ...K) (map[K]V, error) {
	raws, err := e.getStrings(ctx, keys)
	if err != nil {
		return nil, err
	}
	values := make(map[K]V, len(raws))
	for key, raw := range raws {
		value, err := e.decode(e.keyOf(key), raw)
		if errors.Is(err, ErrMiss) {
			continue
		}
		if err != nil {
			return nil, err
		}
		values[key] = value
	}
	return values, nil
}

// Set writes value under the key as JSON, with the entry's TTL unless an option says
// otherwise.
func (e *JSON[K, V]) Set(ctx context.Context, key K, value V, opts ...WriteOption) error {
	encoded, err := codec.EncodeJSON(value)
	if err != nil {
		return fmt.Errorf("ocel: kv entry %q: the value does not encode as JSON: %w", e.name, err)
	}
	return e.setString(ctx, "Set", key, string(encoded), opts)
}

// A List is a list entry of a [Store]: a list of strings under each key.
type List[K comparable] struct{ entry[K] }

// NewList declares a list entry named name on store. Its name, pattern and K are written
// as [NewText]'s are.
func NewList[K comparable](store *Store, name, pattern string, opts ...EntryOption) *List[K] {
	return &List[K]{declareEntry[K](store, name, pattern, resourcesv1.KvShape_KV_SHAPE_LIST, opts)}
}

// PushBack appends values, one or more, to the end of the list and returns its new
// length, applying the entry's TTL in the same transaction unless an option says
// otherwise.
func (e *List[K]) PushBack(ctx context.Context, key K, values []string, opts ...WriteOption) (int64, error) {
	if err := e.refuseNoValues("PushBack", values); err != nil {
		return 0, err
	}
	return e.write(ctx, "PushBack", key, opts, func(transaction redis.Pipeliner, built string) *redis.IntCmd {
		return transaction.RPush(ctx, built, stringsAsArgs(values)...)
	})
}

// PushFront prepends values to the start of the list, keeping their order, and returns
// its new length, as [List.PushBack] does.
func (e *List[K]) PushFront(ctx context.Context, key K, values []string, opts ...WriteOption) (int64, error) {
	if err := e.refuseNoValues("PushFront", values); err != nil {
		return 0, err
	}
	reversed := make([]string, len(values))
	for i, value := range values {
		reversed[len(values)-1-i] = value
	}
	return e.write(ctx, "PushFront", key, opts, func(transaction redis.Pipeliner, built string) *redis.IntCmd {
		return transaction.LPush(ctx, built, stringsAsArgs(reversed)...)
	})
}

func (e entry[K]) refuseNoValues(operation string, values []string) error {
	if len(values) == 0 {
		return fmt.Errorf("ocel: kv entry %q: %s takes one or more values", e.name, operation)
	}
	return nil
}

func stringsAsArgs(values []string) []any {
	args := make([]any, len(values))
	for i, value := range values {
		args[i] = value
	}
	return args
}

func (e *List[K]) runValueCommand(operation string, key K, command func(*redis.Client, string) *redis.StringCmd) (string, error) {
	client, err := e.open(operation)
	if err != nil {
		return "", err
	}
	value, err := command(client, e.keyOf(key)).Result()
	if errors.Is(err, redis.Nil) {
		return "", ErrMiss
	}
	return value, err
}

// PopBack removes and returns the last value, or [ErrMiss] when the list is empty.
func (e *List[K]) PopBack(ctx context.Context, key K) (string, error) {
	return e.runValueCommand("PopBack", key, func(client *redis.Client, built string) *redis.StringCmd { return client.RPop(ctx, built) })
}

// PopFront removes and returns the first value, or [ErrMiss] when the list is empty.
func (e *List[K]) PopFront(ctx context.Context, key K) (string, error) {
	return e.runValueCommand("PopFront", key, func(client *redis.Client, built string) *redis.StringCmd { return client.LPop(ctx, built) })
}

// Index is the value at index, counting back from the end when negative, or [ErrMiss]
// past either end.
func (e *List[K]) Index(ctx context.Context, key K, index int64) (string, error) {
	return e.runValueCommand("Index", key, func(client *redis.Client, built string) *redis.StringCmd { return client.LIndex(ctx, built, index) })
}

// Range is the values from start to stop, both included and counted back from the end
// when negative, so Range(ctx, key, 0, -1) is the whole list.
func (e *List[K]) Range(ctx context.Context, key K, start, stop int64) ([]string, error) {
	client, err := e.open("Range")
	if err != nil {
		return nil, err
	}
	return client.LRange(ctx, e.keyOf(key), start, stop).Result()
}

// Len is how many values the list holds.
func (e *List[K]) Len(ctx context.Context, key K) (int64, error) {
	client, err := e.open("Len")
	if err != nil {
		return 0, err
	}
	return client.LLen(ctx, e.keyOf(key)).Result()
}

// A Set is a set entry of a [Store]: a set of strings under each key.
type Set[K comparable] struct{ entry[K] }

// NewSet declares a set entry named name on store. Its name, pattern and K are written
// as [NewText]'s are.
func NewSet[K comparable](store *Store, name, pattern string, opts ...EntryOption) *Set[K] {
	return &Set[K]{declareEntry[K](store, name, pattern, resourcesv1.KvShape_KV_SHAPE_SET, opts)}
}

// Add adds members, one or more, to the set and returns how many were not in it already,
// applying the entry's TTL in the same transaction unless an option says otherwise.
func (e *Set[K]) Add(ctx context.Context, key K, members []string, opts ...WriteOption) (int64, error) {
	if err := e.refuseNoValues("Add", members); err != nil {
		return 0, err
	}
	return e.write(ctx, "Add", key, opts, func(transaction redis.Pipeliner, built string) *redis.IntCmd {
		return transaction.SAdd(ctx, built, stringsAsArgs(members)...)
	})
}

// Remove removes members, one or more, from the set and returns how many were in it.
func (e *Set[K]) Remove(ctx context.Context, key K, members ...string) (int64, error) {
	if err := e.refuseNoValues("Remove", members); err != nil {
		return 0, err
	}
	client, err := e.open("Remove")
	if err != nil {
		return 0, err
	}
	return client.SRem(ctx, e.keyOf(key), stringsAsArgs(members)...).Result()
}

// Contains reports whether member is in the set.
func (e *Set[K]) Contains(ctx context.Context, key K, member string) (bool, error) {
	client, err := e.open("Contains")
	if err != nil {
		return false, err
	}
	return client.SIsMember(ctx, e.keyOf(key), member).Result()
}

// Len is how many members the set holds.
func (e *Set[K]) Len(ctx context.Context, key K) (int64, error) {
	client, err := e.open("Len")
	if err != nil {
		return 0, err
	}
	return client.SCard(ctx, e.keyOf(key)).Result()
}

// Members is every member of the set, in no order.
func (e *Set[K]) Members(ctx context.Context, key K) ([]string, error) {
	client, err := e.open("Members")
	if err != nil {
		return nil, err
	}
	return client.SMembers(ctx, e.keyOf(key)).Result()
}
