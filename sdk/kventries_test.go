package ocel_test

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"ocel.dev"
)

func TestATextEntryReadsWhatWasWrittenAndMissesAsErrKVMiss(t *testing.T) {
	server := serveKV(t, "text")
	cache := ocel.KV("text")
	greeting := ocel.KVText[struct{}](cache, "greeting", "greeting")
	notes := ocel.KVText[string](cache, "notes", "notes/:id")
	ctx := context.Background()

	if _, err := greeting.Get(ctx, struct{}{}); !errors.Is(err, ocel.ErrKVMiss) {
		t.Errorf("Get() before a write err = %v, want ErrKVMiss", err)
	}
	if err := greeting.Set(ctx, struct{}{}, "hello"); err != nil {
		t.Fatalf("Set() = %v", err)
	}
	if got, err := greeting.Get(ctx, struct{}{}); err != nil || got != "hello" {
		t.Errorf("Get() = %q, %v, want hello", got, err)
	}
	if got, _ := server.Get("greeting"); got != "hello" {
		t.Errorf("server holds %q under greeting, want hello", got)
	}

	_ = notes.Set(ctx, "a", "first")
	many, err := notes.GetMany(ctx, "a", "b")
	if err != nil || !reflect.DeepEqual(many, map[string]string{"a": "first"}) {
		t.Errorf("GetMany(a, b) = %v, %v, want a alone", many, err)
	}
	if deleted, err := notes.Delete(ctx, "a"); err != nil || !deleted {
		t.Errorf("Delete(a) = %v, %v, want true", deleted, err)
	}
	if deleted, _ := notes.Delete(ctx, "a"); deleted {
		t.Errorf("Delete(a) again = true, want false")
	}
}

func TestACounterCountsAtomicallyFromZero(t *testing.T) {
	serveKV(t, "counter")
	cache := ocel.KV("counter")
	hits := ocel.KVCounter[string](cache, "hits", "hits/:page")
	ctx := context.Background()

	if _, err := hits.Get(ctx, "home"); !errors.Is(err, ocel.ErrKVMiss) {
		t.Errorf("Get() before a write err = %v, want ErrKVMiss", err)
	}
	for _, step := range []struct {
		do   func() (int64, error)
		want int64
	}{
		{func() (int64, error) { return hits.Increment(ctx, "home", 1) }, 1},
		{func() (int64, error) { return hits.Increment(ctx, "home", 5) }, 6},
		{func() (int64, error) { return hits.Decrement(ctx, "home", 2) }, 4},
		{func() (int64, error) { return hits.Decrement(ctx, "other", 1) }, -1},
	} {
		if got, err := step.do(); err != nil || got != step.want {
			t.Errorf("step = %d, %v, want %d", got, err, step.want)
		}
	}
	_ = hits.Set(ctx, "set", 10)
	many, err := hits.GetMany(ctx, "home", "set", "none")
	if err != nil || !reflect.DeepEqual(many, map[string]int64{"home": 4, "set": 10}) {
		t.Errorf("GetMany() = %v, %v, want home 4 and set 10", many, err)
	}
}

func TestACounterHoldingNoIntegerIsInvalid(t *testing.T) {
	server := serveKV(t, "badcounter")
	cache := ocel.KV("badcounter")
	hits := ocel.KVCounter[struct{}](cache, "hits", "hits")
	_ = server.Set("hits", "lots")

	var invalid *ocel.InvalidKVValueError
	if _, err := hits.Get(context.Background(), struct{}{}); !errors.As(err, &invalid) || invalid.Key != "hits" {
		t.Errorf("Get() err = %v, want an InvalidKVValueError for hits", err)
	}
}

func TestAJSONEntryDecodesIntoItsTypeOnReadAndRefusesWhatDoesNot(t *testing.T) {
	server := serveKV(t, "json")
	cache := ocel.KV("json")
	sessions := ocel.KVJSON[string, session](cache, "session", "session/:id")
	lenient := ocel.KVJSON[string, session](cache, "lenient", "lenient/:id", ocel.KVMissOnInvalid())
	ctx := context.Background()

	if err := sessions.Set(ctx, "s1", session{User: "ada", Visits: 2}); err != nil {
		t.Fatalf("Set() = %v", err)
	}
	if got, err := sessions.Get(ctx, "s1"); err != nil || got != (session{User: "ada", Visits: 2}) {
		t.Errorf("Get(s1) = %+v, %v, want ada with 2 visits", got, err)
	}
	if _, err := sessions.Get(ctx, "none"); !errors.Is(err, ocel.ErrKVMiss) {
		t.Errorf("Get(none) err = %v, want ErrKVMiss", err)
	}

	for key, stored := range map[string]string{
		"s3": `{"user":7}`,
		"s4": "not json",
		"s5": "null",
		"s6": `{"user":"ada","visits":2,"admin":true}`,
		"s7": `{"user":"ada","visits":2} {"user":"bob"}`,
	} {
		_ = server.Set("session/"+key, stored)
		_ = server.Set("lenient/"+key, stored)
		var invalid *ocel.InvalidKVValueError
		if _, err := sessions.Get(ctx, key); !errors.As(err, &invalid) {
			t.Errorf("Get(%s) err = %v, want an InvalidKVValueError", key, err)
		}
		if _, err := lenient.Get(ctx, key); !errors.Is(err, ocel.ErrKVMiss) {
			t.Errorf("lenient Get(%s) err = %v, want ErrKVMiss", key, err)
		}
	}
	if many, err := lenient.GetMany(ctx, "s3", "s4", "s5", "s6", "s7"); err != nil || len(many) != 0 {
		t.Errorf("lenient GetMany() = %v, %v, want every one read as a miss", many, err)
	}
}

func TestMissOnInvalidIsRefusedForAnythingButJSON(t *testing.T) {
	cache := ocel.KV("missoninvalid")

	if message := panicOf(func() { ocel.KVText[string](cache, "t", "t/:id", ocel.KVMissOnInvalid()) }); message == "" {
		t.Error("KVText with KVMissOnInvalid did not panic, want it refused")
	}
}

func TestAListKeepsItsValuesInOrder(t *testing.T) {
	serveKV(t, "list")
	cache := ocel.KV("list")
	recent := ocel.KVList[string](cache, "recent", "recent/:user")
	ctx := context.Background()

	if n, err := recent.PushBack(ctx, "ada", []string{"b", "c"}); err != nil || n != 2 {
		t.Errorf("PushBack() = %d, %v, want 2", n, err)
	}
	if n, _ := recent.PushFront(ctx, "ada", []string{"z", "a"}); n != 4 {
		t.Errorf("PushFront() = %d, want 4", n)
	}
	if got, _ := recent.Range(ctx, "ada", 0, -1); !slices.Equal(got, []string{"z", "a", "b", "c"}) {
		t.Errorf("Range(0, -1) = %v, want z a b c", got)
	}
	if got, _ := recent.Range(ctx, "ada", 1, 2); !slices.Equal(got, []string{"a", "b"}) {
		t.Errorf("Range(1, 2) = %v, want a b", got)
	}
	if got, err := recent.Index(ctx, "ada", -1); err != nil || got != "c" {
		t.Errorf("Index(-1) = %q, %v, want c", got, err)
	}
	if _, err := recent.Index(ctx, "ada", 9); !errors.Is(err, ocel.ErrKVMiss) {
		t.Errorf("Index(9) err = %v, want ErrKVMiss", err)
	}
	if got, _ := recent.PopBack(ctx, "ada"); got != "c" {
		t.Errorf("PopBack() = %q, want c", got)
	}
	if got, _ := recent.PopFront(ctx, "ada"); got != "z" {
		t.Errorf("PopFront() = %q, want z", got)
	}
	if n, _ := recent.Len(ctx, "ada"); n != 2 {
		t.Errorf("Len() = %d, want 2", n)
	}
	if deleted, _ := recent.Delete(ctx, "ada"); !deleted {
		t.Error("Delete() = false, want true")
	}
	if _, err := recent.PopBack(ctx, "ada"); !errors.Is(err, ocel.ErrKVMiss) {
		t.Errorf("PopBack() of an empty list err = %v, want ErrKVMiss", err)
	}
}

func TestASetKeepsDistinctMembers(t *testing.T) {
	serveKV(t, "set")
	cache := ocel.KV("set")
	online := ocel.KVSet[string](cache, "online", "online/:room")
	ctx := context.Background()

	if n, _ := online.Add(ctx, "lobby", []string{"ada", "bob"}); n != 2 {
		t.Errorf("Add() = %d, want 2", n)
	}
	if n, _ := online.Add(ctx, "lobby", []string{"ada"}); n != 0 {
		t.Errorf("Add(ada) again = %d, want 0", n)
	}
	if in, _ := online.Contains(ctx, "lobby", "ada"); !in {
		t.Error("Contains(ada) = false, want true")
	}
	if n, _ := online.Len(ctx, "lobby"); n != 2 {
		t.Errorf("Len() = %d, want 2", n)
	}
	members, _ := online.Members(ctx, "lobby")
	slices.Sort(members)
	if !slices.Equal(members, []string{"ada", "bob"}) {
		t.Errorf("Members() = %v, want ada bob", members)
	}
	if n, _ := online.Remove(ctx, "lobby", "ada"); n != 1 {
		t.Errorf("Remove(ada) = %d, want 1", n)
	}
	if deleted, _ := online.Delete(ctx, "lobby"); !deleted {
		t.Error("Delete() = false, want true")
	}
}

func TestAnEmptyListOrSetWriteIsRefusedBeforeReachingTheStore(t *testing.T) {
	cache := ocel.KV("emptywrite")
	recent := ocel.KVList[string](cache, "recent", "recent/:id")
	online := ocel.KVSet[string](cache, "online", "online/:id")
	ctx := context.Background()

	for operation, write := range map[string]func() error{
		"PushBack":  func() error { _, err := recent.PushBack(ctx, "a", nil); return err },
		"PushFront": func() error { _, err := recent.PushFront(ctx, "a", []string{}); return err },
		"Add":       func() error { _, err := online.Add(ctx, "a", nil); return err },
		"Remove":    func() error { _, err := online.Remove(ctx, "a"); return err },
	} {
		err := write()
		var missing *ocel.MissingBindingError
		if err == nil || errors.As(err, &missing) || !strings.Contains(err.Error(), operation+" takes one or more values") {
			t.Errorf("%s() with no values err = %v, want it refused before the binding is read", operation, err)
		}
	}
}

func TestADeclaredTTLIsAppliedWithEveryWrite(t *testing.T) {
	server := serveKV(t, "ttl")
	cache := ocel.KV("ttl")
	ctx := context.Background()
	sessions := ocel.KVText[string](cache, "session", "session/:id", ocel.KVTTL(30*24*time.Hour))
	hits := ocel.KVCounter[string](cache, "hits", "hits/:id", ocel.KVTTL(10*time.Second))
	recent := ocel.KVList[string](cache, "recent", "recent/:id", ocel.KVTTL(time.Hour))
	online := ocel.KVSet[string](cache, "online", "online/:id", ocel.KVTTL(5*time.Minute))

	_ = sessions.Set(ctx, "a", "x")
	_, _ = hits.Increment(ctx, "a", 1)
	_, _ = recent.PushBack(ctx, "a", []string{"x"})
	_, _ = online.Add(ctx, "a", []string{"x"})

	for key, want := range map[string]time.Duration{
		"session/a": 30 * 24 * time.Hour,
		"hits/a":    10 * time.Second,
		"recent/a":  time.Hour,
		"online/a":  5 * time.Minute,
	} {
		if got := server.TTL(key); got != want {
			t.Errorf("TTL(%s) = %v, want %v", key, got, want)
		}
	}
}

func TestAWriteOverridesKeepsOrClearsTheTTL(t *testing.T) {
	server := serveKV(t, "ttlwrite")
	cache := ocel.KV("ttlwrite")
	ctx := context.Background()
	sessions := ocel.KVText[string](cache, "session", "session/:id", ocel.KVTTL(30*24*time.Hour))
	hits := ocel.KVCounter[string](cache, "hits", "hits/:id", ocel.KVTTL(10*time.Second))
	forever := ocel.KVText[string](cache, "forever", "forever/:id")

	_ = sessions.Set(ctx, "o", "x", ocel.KVTTL(5*time.Second))
	if got := server.TTL("session/o"); got != 5*time.Second {
		t.Errorf("TTL after KVTTL(5s) = %v, want 5s", got)
	}
	_ = sessions.Set(ctx, "o", "y", ocel.KVKeepTTL())
	if got := server.TTL("session/o"); got != 5*time.Second {
		t.Errorf("TTL after KVKeepTTL = %v, want 5s kept", got)
	}
	_ = sessions.Set(ctx, "o", "z", ocel.KVNoTTL())
	if got := server.TTL("session/o"); got != 0 {
		t.Errorf("TTL after KVNoTTL = %v, want none", got)
	}

	_, _ = hits.Increment(ctx, "o", 1, ocel.KVTTL(time.Minute))
	_, _ = hits.Increment(ctx, "o", 1, ocel.KVKeepTTL())
	if got := server.TTL("hits/o"); got != time.Minute {
		t.Errorf("TTL after an increment keeping it = %v, want 1m", got)
	}
	_, _ = hits.Decrement(ctx, "o", 1, ocel.KVNoTTL())
	if got := server.TTL("hits/o"); got != 0 {
		t.Errorf("TTL after a decrement clearing it = %v, want none", got)
	}

	_ = server.Set("forever/a", "x")
	server.SetTTL("forever/a", time.Second)
	_ = forever.Set(ctx, "a", "y")
	if got := server.TTL("forever/a"); got != 0 {
		t.Errorf("TTL after a write to an entry with no TTL = %v, want none", got)
	}
}

func TestTheKVFixtureValuesAreStoredAsEverySDKStoresThem(t *testing.T) {
	fixture := readKVFixture(t)
	server := serveKV(t, "values")
	cache := ocel.KV("values")
	ctx := context.Background()
	none := struct{}{}
	text := ocel.KVText[struct{}](cache, "text", "text")
	counter := ocel.KVCounter[struct{}](cache, "counter", "counter")
	sessions := ocel.KVJSON[struct{}, fixtureSession](cache, "json", "json")
	list := ocel.KVList[struct{}](cache, "list", "list")
	set := ocel.KVSet[struct{}](cache, "set", "set")

	for _, tc := range fixture.Values.Text {
		_ = text.Set(ctx, none, tc.Value)
		if got, _ := server.Get("text"); got != tc.Stored {
			t.Errorf("text stored %q, want %q", got, tc.Stored)
		}
		_ = server.Set("text", tc.Stored)
		if got, err := text.Get(ctx, none); err != nil || got != tc.Value {
			t.Errorf("text read %q, %v, want %q", got, err, tc.Value)
		}
	}
	for _, tc := range fixture.Values.Counter {
		_ = counter.Set(ctx, none, tc.Value)
		if got, _ := server.Get("counter"); got != tc.Stored {
			t.Errorf("counter stored %q, want %q", got, tc.Stored)
		}
		_ = server.Set("counter", tc.Stored)
		if got, err := counter.Get(ctx, none); err != nil || got != tc.Value {
			t.Errorf("counter read %d, %v, want %d", got, err, tc.Value)
		}
	}
	for _, tc := range fixture.Values.JSON {
		_ = sessions.Set(ctx, none, tc.Value)
		if got, _ := server.Get("json"); got != tc.Stored {
			t.Errorf("json stored %s, want %s", got, tc.Stored)
		}
		_ = server.Set("json", tc.Stored)
		if got, err := sessions.Get(ctx, none); err != nil || !slices.Equal(got.Roles, tc.Value.Roles) || got.User != tc.Value.User || got.Note != tc.Value.Note || got.Visits != tc.Value.Visits {
			t.Errorf("json read %+v, %v, want %+v", got, err, tc.Value)
		}
	}
	for _, tc := range fixture.Values.List {
		_, _ = list.PushBack(ctx, none, tc.Value)
		if got, _ := server.List("list"); !slices.Equal(got, tc.Stored) {
			t.Errorf("list stored %q, want %q", got, tc.Stored)
		}
		server.Del("list")
		_, _ = server.Push("list", tc.Stored...)
		if got, err := list.Range(ctx, none, 0, -1); err != nil || !slices.Equal(got, tc.Value) {
			t.Errorf("list read %q, %v, want %q", got, err, tc.Value)
		}
	}
	for _, tc := range fixture.Values.Set {
		_, _ = set.Add(ctx, none, tc.Value)
		if got, _ := server.Members("set"); !slices.Equal(got, tc.Stored) {
			t.Errorf("set stored %q, want %q", got, tc.Stored)
		}
		server.Del("set")
		_, _ = server.SetAdd("set", tc.Stored...)
		got, err := set.Members(ctx, none)
		slices.Sort(got)
		if err != nil || !slices.Equal(got, tc.Value) {
			t.Errorf("set read %q, %v, want %q", got, err, tc.Value)
		}
	}
}
