package googletest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	crm "google.golang.org/api/cloudresourcemanager/v3"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
)

type TierTags struct {
	Mu       sync.Mutex
	URL      string
	Keys     map[string]*crm.TagKey
	Values   map[string]*crm.TagValue
	Bindings map[string][]string

	BindingsMade    []string
	BindingsAsked   int
	BindingsRemoved []string
	KeysMade        int
	ValuesMade      int
	ValueLookups    int

	BindingsUnseen int
	BindingsDenied bool
	ValuesAbsent   bool
	Denied         []string

	next int
}

func NewTierTags(t *testing.T) *TierTags {
	t.Helper()
	tags := &TierTags{Keys: map[string]*crm.TagKey{}, Values: map[string]*crm.TagValue{}, Bindings: map[string][]string{}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { tags.Serve(t, w, r) }))
	t.Cleanup(server.Close)
	tags.URL = server.URL
	return tags
}

func NewTieredTags(t *testing.T) *TierTags {
	t.Helper()
	tags := NewTierTags(t)
	key := &crm.TagKey{Name: "tagKeys/1", Parent: "projects/123456789", ShortName: names.TierTagKey, NamespacedName: Project + "/" + names.TierTagKey}
	tags.Keys[key.NamespacedName] = key
	for _, tier := range BothTiers {
		tags.addValue(key, string(tier))
	}
	return tags
}

func (s *TierTags) Tagged(c *gcloud.Clients) *gcloud.Clients {
	c.TierTagEndpoint = s.URL
	return c
}

func (s *TierTags) ValueOf(tier environment.Tier) string {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	if value := s.Values[Project+"/"+names.TierTagKey+"/"+string(tier)]; value != nil {
		return value.Name
	}
	return ""
}

func (s *TierTags) Bound(parent string) []string {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	return slices.Clone(s.Bindings[parent])
}

func (s *TierTags) BindInline(parent string, tags map[string]string) {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	for key, short := range tags {
		if value := s.Values[key+"/"+short]; value != nil {
			s.Bindings[parent] = append(s.Bindings[parent], value.Name)
		}
	}
}

func (s *TierTags) addValue(key *crm.TagKey, short string) *crm.TagValue {
	s.next++
	value := &crm.TagValue{
		Name:           "tagValues/" + strconv.Itoa(100+s.next),
		Parent:         key.Name,
		ShortName:      short,
		NamespacedName: key.NamespacedName + "/" + short,
	}
	s.Values[value.NamespacedName] = value
	return value
}

func (s *TierTags) Serve(t *testing.T, w http.ResponseWriter, r *http.Request) {
	t.Helper()
	s.Mu.Lock()
	defer s.Mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	path := strings.TrimPrefix(r.URL.EscapedPath(), "/v3/")
	switch {
	case r.Method == http.MethodPost && path == "/v1/projects/"+Project+":testIamPermissions":
		asked := ReadBody[crm.TestIamPermissionsRequest](w, r)
		if asked == nil {
			return
		}
		granted := &crm.TestIamPermissionsResponse{}
		for _, permission := range asked.Permissions {
			if !slices.Contains(s.Denied, permission) {
				granted.Permissions = append(granted.Permissions, permission)
			}
		}
		WriteBody(w, granted)
	case r.Method == http.MethodGet && path == "tagKeys/namespaced":
		key := s.Keys[r.URL.Query().Get("name")]
		if key == nil || slices.Contains(s.Denied, "resourcemanager.tagKeys.get") {
			writeDenied(w)
			return
		}
		WriteBody(w, key)
	case r.Method == http.MethodGet && path == "tagValues/namespaced":
		s.ValueLookups++
		value := s.Values[r.URL.Query().Get("name")]
		if value == nil || s.ValuesAbsent || slices.Contains(s.Denied, "resourcemanager.tagValues.get") {
			writeDenied(w)
			return
		}
		WriteBody(w, value)
	case r.Method == http.MethodPost && path == "tagKeys":
		asked := ReadBody[crm.TagKey](w, r)
		if asked == nil {
			return
		}
		s.next++
		key := &crm.TagKey{Name: "tagKeys/" + strconv.Itoa(s.next), Parent: asked.Parent, ShortName: asked.ShortName, Description: asked.Description,
			NamespacedName: strings.TrimPrefix(asked.Parent, "projects/") + "/" + asked.ShortName}
		if s.Keys[key.NamespacedName] != nil {
			writeConflict(w)
			return
		}
		s.Keys[key.NamespacedName] = key
		s.KeysMade++
		writeDoneOperation(w, key)
	case r.Method == http.MethodPost && path == "tagValues":
		asked := ReadBody[crm.TagValue](w, r)
		if asked == nil {
			return
		}
		var key *crm.TagKey
		for _, each := range s.Keys {
			if each.Name == asked.Parent {
				key = each
			}
		}
		if key == nil {
			WriteNotFound(w)
			return
		}
		if s.Values[key.NamespacedName+"/"+asked.ShortName] != nil {
			writeConflict(w)
			return
		}
		s.ValuesMade++
		writeDoneOperation(w, s.addValue(key, asked.ShortName))
	case r.Method == http.MethodPost && path == "tagBindings":
		asked := ReadBody[crm.TagBinding](w, r)
		if asked == nil {
			return
		}
		s.BindingsAsked++
		if s.BindingsDenied {
			writeDenied(w)
			return
		}
		if s.BindingsUnseen > 0 {
			s.BindingsUnseen--
			WriteNotFound(w)
			return
		}
		held := s.Bindings[asked.Parent]
		if slices.Contains(held, asked.TagValue) {
			writeConflict(w)
			return
		}
		if len(held) > 0 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"code":400,"status":"FAILED_PRECONDITION","message":"A binding already exists between the given resource and a TagValue parented by the same TagKey as the given TagValue."}}`))
			return
		}
		s.Bindings[asked.Parent] = append(held, asked.TagValue)
		s.BindingsMade = append(s.BindingsMade, asked.Parent)
		writeDoneOperation(w, &crm.TagBinding{Name: bindingName(asked.Parent, asked.TagValue), Parent: asked.Parent, TagValue: asked.TagValue})
	case r.Method == http.MethodGet && path == "tagBindings":
		parent := r.URL.Query().Get("parent")
		listed := &crm.ListTagBindingsResponse{}
		for _, value := range s.Bindings[parent] {
			listed.TagBindings = append(listed.TagBindings, &crm.TagBinding{Name: bindingName(parent, value), Parent: parent, TagValue: value})
		}
		WriteBody(w, listed)
	case r.Method == http.MethodDelete && strings.HasPrefix(path, "tagBindings/"):
		name, err := url.PathUnescape(path)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		escapedParent, value, found := strings.Cut(strings.TrimPrefix(name, "tagBindings/"), "/tagValues/")
		parent, err := url.PathUnescape(escapedParent)
		if !found || err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		held := s.Bindings[parent]
		at := slices.Index(held, "tagValues/"+value)
		if at < 0 {
			WriteNotFound(w)
			return
		}
		s.Bindings[parent] = slices.Delete(held, at, at+1)
		if len(s.Bindings[parent]) == 0 {
			delete(s.Bindings, parent)
		}
		s.BindingsRemoved = append(s.BindingsRemoved, parent)
		writeDoneOperation(w, struct{}{})
	default:
		t.Errorf("the Resource Manager fake was asked %s %s, which it does not serve", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotImplemented)
	}
}

func bindingName(parent, value string) string {
	return "tagBindings/" + url.PathEscape(parent) + "/" + value
}

func writeDoneOperation(w http.ResponseWriter, response any) {
	encoded, err := json.Marshal(response)
	if err != nil {
		panic(err)
	}
	WriteBody(w, &crm.Operation{Name: "operations/done", Done: true, Response: encoded})
}

func writeConflict(w http.ResponseWriter) {
	w.WriteHeader(http.StatusConflict)
	_, _ = w.Write([]byte(`{"error":{"code":409,"status":"ALREADY_EXISTS","message":"already exists"}}`))
}

func writeDenied(w http.ResponseWriter) {
	w.WriteHeader(http.StatusForbidden)
	_, _ = w.Write([]byte(`{"error":{"code":403,"status":"PERMISSION_DENIED","message":"Permission denied on resource (or it may not exist)."}}`))
}
