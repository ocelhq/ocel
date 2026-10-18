package gcp

import (
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/ocelhq/ocel/platform/gcp/provider/internal/run"
	runv1 "google.golang.org/api/run/v1"
	runv2 "google.golang.org/api/run/v2"
)

type runServer struct {
	mu        sync.Mutex
	service   *runv2.GoogleCloudRunV2Service
	revision  int
	revisions []string
	writes    int

	created  []*runv2.GoogleCloudRunV2Service
	patched  []*runv2.GoogleCloudRunV2Service
	policies []*runv2.GoogleIamV1Policy

	patchConflicts int
	onConflict     func(*runv2.GoogleCloudRunV2Service)
	patchTries     int

	uriEachRevision bool
	failRouting     bool
	refuseReads     bool
	onCreate        func()

	uploads []upload
	present map[string]bool
	events  []string

	iam     *iamServer
	secrets *secretServer

	images    map[string]string
	labels    map[string]map[string]string
	elsewhere map[string]string
	untagged  []string
	regional  int
	asked     []string
	missing   []string
	retagged  []string
	versions  map[string]string
	onUntag   func(tag string)

	untagAtRelease    string
	untagEveryRelease bool

	tagged map[string][]string
}

type upload struct {
	name, ifGenerationMatch string
	body                    []byte
	contentType             string
	cacheControl            string
}

func (s *runServer) open(t *testing.T) *Provider {
	t.Helper()
	return pushing(t, s.identities().serve(t, s.serve(t)).Endpoint)
}

func (s *runServer) identities() *iamServer {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.iam == nil {
		s.iam = grantedIAM()
	}
	return s.iam
}

func (s *runServer) serve(t *testing.T) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		path := r.URL.Path
		if s.iam == nil {
			s.iam = grantedIAM()
		}
		switch {
		case strings.HasPrefix(path, "/v1/projects/") && strings.Contains(path, "/secrets"):
			if s.secrets == nil {
				s.secrets = newSecretServer()
			}
			s.secrets.serve(t, w, r)
		case strings.HasPrefix(path, "/v1/") && !strings.Contains(path, "/packages/"):
			s.iam.rest(t)(w, r)
		case r.Method == http.MethodPost && strings.HasPrefix(path, "/upload/storage/v1/b/"):
			s.store(t, w, r)
		case r.Method == http.MethodPost && strings.HasSuffix(path, ":setIamPolicy"):
			s.setPolicy(w, r)
		case r.Method == http.MethodGet && strings.HasSuffix(path, ":getIamPolicy"):
			writeBody(w, &runv2.GoogleIamV1Policy{Etag: "BwXhoLA="})
		case r.Method == http.MethodPost && strings.HasSuffix(path, "/services"):
			s.create(w, r)
		case r.Method == http.MethodPatch:
			s.patch(w, r)
		case r.Method == http.MethodDelete && strings.Contains(path, "/packages/") && strings.Contains(path, "/tags/"):
			s.untagged = append(s.untagged, strings.TrimPrefix(r.URL.EscapedPath(), "/v1/"))
			if s.onUntag != nil {
				s.onUntag(strings.TrimPrefix(r.URL.EscapedPath(), "/v1/"))
			}
			writeBody(w, map[string]any{})
		case r.Method == http.MethodDelete && strings.Contains(path, "/revisions/"):
			s.deleteRevision(w, run.RevisionName(path))
		case r.Method == http.MethodGet && strings.HasSuffix(path, "/services/-/revisions"):
			s.listRegionRevisions(w)
		case r.Method == http.MethodGet && strings.HasPrefix(path, "/apis/serving.knative.dev/v1/namespaces/") && strings.HasSuffix(path, "/revisions"):
			s.listLabelled(w, r.URL.Query().Get("labelSelector"))
		case r.Method == http.MethodGet && strings.Contains(path, "/packages/") && strings.Contains(path, "/tags/"):
			s.getTag(w, strings.TrimPrefix(r.URL.EscapedPath(), "/v1/"))
		case r.Method == http.MethodGet && strings.Contains(path, "/packages/") && strings.HasSuffix(path, "/tags"):
			s.listTags(w, strings.TrimPrefix(r.URL.EscapedPath(), "/v1/"))
		case r.Method == http.MethodPost && strings.Contains(path, "/packages/") && strings.HasSuffix(path, "/tags"):
			s.createTag(w, r)
		case r.Method == http.MethodGet && strings.HasSuffix(path, "/revisions"):
			s.listRevisions(w)
		case r.Method == http.MethodGet && strings.Contains(path, "/revisions/"):
			s.getRevision(w, run.RevisionName(path))
		case r.Method == http.MethodDelete:
			s.service = nil
			s.revisions = nil
			writeBody(w, &runv2.GoogleLongrunningOperation{Name: "operations/delete", Done: true})
		case r.Method == http.MethodGet && strings.Contains(path, "/operations/"):
			writeBody(w, &runv2.GoogleLongrunningOperation{Name: strings.TrimPrefix(path, "/v2/"), Done: true})
		case r.Method == http.MethodGet && strings.HasPrefix(path, "/storage/v1/b/") && strings.Contains(path, "/o/"):
			s.objectAttrs(w, path)
		case r.Method == http.MethodGet:
			s.get(w)
		default:
			t.Errorf("the release called %s %s, which nothing here serves", r.Method, path)
			w.WriteHeader(http.StatusNotFound)
		}
	}
}

func (s *runServer) create(w http.ResponseWriter, r *http.Request) {
	desired := readBody[runv2.GoogleCloudRunV2Service](w, r)
	if desired == nil {
		return
	}
	if s.onCreate != nil {
		s.onCreate()
	}
	s.created = append(s.created, desired)
	s.events = append(s.events, "create")
	name := r.URL.Query().Get("serviceId")
	s.service = &runv2.GoogleCloudRunV2Service{
		Name:               strings.TrimPrefix(r.URL.Path, "/v2/") + "/" + name,
		Template:           desired.Template,
		Traffic:            allocated(desired.Traffic),
		Ingress:            desired.Ingress,
		InvokerIamDisabled: desired.InvokerIamDisabled,
		Labels:             desired.Labels,
		CustomAudiences:    desired.CustomAudiences,
		Uri:                "https://" + name + ".run.app",
	}
	if failed := s.untagUnderRelease(desired.Template); failed != nil {
		writeBody(w, failed)
		return
	}
	s.revised()
	writeBody(w, &runv2.GoogleLongrunningOperation{Name: "operations/create", Done: true})
}

func (s *runServer) patch(w http.ResponseWriter, r *http.Request) {
	desired := readBody[runv2.GoogleCloudRunV2Service](w, r)
	if desired == nil {
		return
	}
	s.patchTries++
	if desired.Etag != "" && desired.Etag != s.service.Etag {
		conflicted(w, "the service's etag is "+s.service.Etag+", not the "+desired.Etag+" this write was based on")
		return
	}
	if s.patchConflicts > 0 {
		s.patchConflicts--
		if s.onConflict != nil {
			s.onConflict(s.service)
		}
		conflicted(w, "the service was changed under this release")
		return
	}
	s.patched = append(s.patched, desired)
	if mask := r.URL.Query().Get("updateMask"); mask != "" {
		fields := strings.Split(mask, ",")
		if slices.Contains(fields, "traffic") {
			s.service.Traffic = allocated(desired.Traffic)
			s.service.TrafficStatuses = taggedStatuses(s.service)
		}
		if slices.Contains(fields, "invoker_iam_disabled") {
			s.service.InvokerIamDisabled = desired.InvokerIamDisabled
		}
		if slices.Contains(fields, "annotations") {
			s.service.Annotations = desired.Annotations
		}
		s.writes++
		s.service.Etag = "etag-" + strconv.Itoa(s.writes)
		if s.failRouting {
			writeBody(w, &runv2.GoogleLongrunningOperation{Name: "operations/release", Done: true, Error: &runv2.GoogleRpcStatus{Message: "the operation failed"}})
			return
		}
		writeBody(w, &runv2.GoogleLongrunningOperation{Name: "operations/release", Done: true})
		return
	}
	replaced := !sameTemplate(s.service.Template, desired.Template)
	s.service.Template = desired.Template
	if failed := s.untagUnderRelease(desired.Template); failed != nil {
		writeBody(w, failed)
		return
	}
	s.service.Traffic = allocated(desired.Traffic)
	s.service.Ingress = desired.Ingress
	s.service.InvokerIamDisabled = desired.InvokerIamDisabled
	s.service.Labels = desired.Labels
	s.service.CustomAudiences = desired.CustomAudiences
	s.service.Annotations = desired.Annotations
	s.writes++
	s.service.Etag = "etag-" + strconv.Itoa(s.writes)
	if replaced {
		s.revised()
	}
	writeBody(w, &runv2.GoogleLongrunningOperation{Name: "operations/release", Done: true})
}

func (s *runServer) untagUnderRelease(template *runv2.GoogleCloudRunV2RevisionTemplate) *runv2.GoogleLongrunningOperation {
	if s.untagAtRelease == "" {
		return nil
	}
	s.missing = append(s.missing, s.untagAtRelease)
	if !s.untagEveryRelease {
		s.untagAtRelease = ""
	}
	return &runv2.GoogleLongrunningOperation{Name: "operations/release", Done: true, Error: &runv2.GoogleRpcStatus{
		Code:    9,
		Message: "Revision '" + run.RevisionName(s.service.Name) + "-failed' is not ready and cannot serve traffic. Image '" + template.Containers[0].Image + "' not found.",
	}}
}

func (s *runServer) revised() {
	s.revision++
	s.writes++
	s.service.LatestReadyRevision = s.service.Name + "/revisions/" + run.RevisionName(s.service.Name) + "-0000" + strconv.Itoa(s.revision)
	if s.uriEachRevision {
		s.service.Uri = "https://" + run.RevisionName(s.service.Name) + "-" + strconv.Itoa(s.revision) + ".run.app"
	}
	s.service.Etag = "etag-" + strconv.Itoa(s.writes)
	s.revisions = append(s.revisions, run.RevisionName(s.service.LatestReadyRevision))
	if s.images == nil {
		s.images = map[string]string{}
	}
	if s.service.Template != nil && len(s.service.Template.Containers) > 0 {
		s.images[run.RevisionName(s.service.LatestReadyRevision)] = s.service.Template.Containers[0].Image
	}
	if s.labels == nil {
		s.labels = map[string]map[string]string{}
	}
	if s.service.Template != nil {
		s.labels[run.RevisionName(s.service.LatestReadyRevision)] = s.service.Template.Labels
	}
}

func (s *runServer) listLabelled(w http.ResponseWriter, selector string) {
	s.asked = append(s.asked, selector)
	key, value, _ := strings.Cut(selector, "=")
	listed := &runv1.ListRevisionsResponse{}
	if s.service != nil {
		for _, name := range s.revisions {
			if s.labels[name][key] == value {
				listed.Items = append(listed.Items, &runv1.Revision{Metadata: &runv1.ObjectMeta{Name: name}})
			}
		}
	}
	for name, image := range s.elsewhere {
		if run.ImageLabelValue(image) == value {
			listed.Items = append(listed.Items, &runv1.Revision{Metadata: &runv1.ObjectMeta{Name: name}})
		}
	}
	writeBody(w, listed)
}

func (s *runServer) getTag(w http.ResponseWriter, name string) {
	unescaped, _ := url.PathUnescape(name)
	if slices.Contains(s.missing, unescaped) || slices.Contains(s.untagged, name) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":{"code":404,"message":"tag not found"}}`))
		return
	}
	version := s.versions[unescaped]
	if version == "" {
		at := strings.LastIndex(unescaped, "/tags/")
		version = unescaped[:at] + "/versions/sha256:" + strings.TrimPrefix(unescaped[at+len("/tags/"):], "sha256-")
	}
	writeBody(w, map[string]any{"name": unescaped, "version": version})
}

func (s *runServer) listTags(w http.ResponseWriter, parent string) {
	listed := map[string]any{}
	var tags []map[string]string
	for _, tag := range s.tagged[strings.TrimSuffix(parent, "/tags")] {
		name := strings.TrimSuffix(parent, "/tags") + "/tags/" + tag
		unescaped, _ := url.PathUnescape(name)
		if !slices.Contains(s.missing, unescaped) && !slices.Contains(s.untagged, name) {
			tags = append(tags, map[string]string{"name": name})
		}
	}
	listed["tags"] = tags
	writeBody(w, listed)
}

func (s *runServer) createTag(w http.ResponseWriter, r *http.Request) {
	tag := readBody[map[string]any](w, r)
	if tag == nil {
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/v1/") + "/" + r.URL.Query().Get("tagId")
	s.retagged = append(s.retagged, name+" -> "+fmt.Sprint((*tag)["version"]))
	s.missing = slices.DeleteFunc(s.missing, func(missing string) bool { return missing == name })
	writeBody(w, tag)
}

func (s *runServer) labelQueries() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.asked)
}

func (s *runServer) retags() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.retagged)
}

func (s *runServer) revisionOf(name string) *runv2.GoogleCloudRunV2Revision {
	return &runv2.GoogleCloudRunV2Revision{
		Name:       s.service.Name + "/revisions/" + name,
		Containers: []*runv2.GoogleCloudRunV2Container{{Image: s.images[name]}},
	}
}

func (s *runServer) getRevision(w http.ResponseWriter, name string) {
	if s.service == nil || !slices.Contains(s.revisions, name) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":{"code":404,"message":"revision not found"}}`))
		return
	}
	writeBody(w, s.revisionOf(name))
}

func (s *runServer) listRevisions(w http.ResponseWriter) {
	listed := &runv2.GoogleCloudRunV2ListRevisionsResponse{}
	for _, name := range s.revisions {
		listed.Revisions = append(listed.Revisions, s.revisionOf(name))
	}
	writeBody(w, listed)
}

func (s *runServer) listRegionRevisions(w http.ResponseWriter) {
	s.regional++
	listed := &runv2.GoogleCloudRunV2ListRevisionsResponse{}
	if s.service != nil {
		for _, name := range s.revisions {
			listed.Revisions = append(listed.Revisions, s.revisionOf(name))
		}
	}
	for name, image := range s.elsewhere {
		listed.Revisions = append(listed.Revisions, &runv2.GoogleCloudRunV2Revision{
			Name:       "projects/acme/locations/europe-west1/services/elsewhere/revisions/" + name,
			Containers: []*runv2.GoogleCloudRunV2Container{{Image: image}},
		})
	}
	writeBody(w, listed)
}

func (s *runServer) regionListings() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.regional
}

func (s *runServer) untags() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.untagged)
}

func (s *runServer) deleteRevision(w http.ResponseWriter, revision string) {
	if s.service == nil || !slices.Contains(s.revisions, revision) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":{"code":404,"message":"revision not found"}}`))
		return
	}
	routed := slices.ContainsFunc(s.service.Traffic, func(target *runv2.GoogleCloudRunV2TrafficTarget) bool {
		return run.RevisionName(target.Revision) == revision ||
			target.Type == run.TrafficByLatest && run.RevisionName(s.service.LatestReadyRevision) == revision
	})
	if routed || run.RevisionName(s.service.LatestReadyRevision) == revision {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":{"code":400,"status":"FAILED_PRECONDITION","message":"revision ` + revision + ` is the latest or can receive traffic"}}`))
		return
	}
	s.revisions = slices.DeleteFunc(s.revisions, func(name string) bool { return name == revision })
	writeBody(w, &runv2.GoogleLongrunningOperation{Name: "operations/delete-revision", Done: true})
}

func taggedStatuses(service *runv2.GoogleCloudRunV2Service) []*runv2.GoogleCloudRunV2TrafficTargetStatus {
	var statuses []*runv2.GoogleCloudRunV2TrafficTargetStatus
	for _, target := range service.Traffic {
		if target.Tag == "" {
			continue
		}
		statuses = append(statuses, &runv2.GoogleCloudRunV2TrafficTargetStatus{
			Type: target.Type, Revision: target.Revision, Percent: target.Percent, Tag: target.Tag,
			Uri: "https://" + target.Tag + "---" + strings.TrimPrefix(service.Uri, "https://"),
		})
	}
	return statuses
}

func allocated(traffic []*runv2.GoogleCloudRunV2TrafficTarget) []*runv2.GoogleCloudRunV2TrafficTarget {
	if len(traffic) > 0 {
		return traffic
	}
	return []*runv2.GoogleCloudRunV2TrafficTarget{{Type: run.TrafficByLatest, Percent: 100}}
}

func sameTemplate(current, desired *runv2.GoogleCloudRunV2RevisionTemplate) bool {
	was, _ := json.Marshal(current)
	is, _ := json.Marshal(desired)
	return string(was) == string(is)
}

func (s *runServer) get(w http.ResponseWriter) {
	if s.refuseReads {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"error":{"code":403,"message":"permission denied on the service"}}`))
		return
	}
	if s.service == nil {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":{"code":404,"message":"service not found"}}`))
		return
	}
	writeBody(w, s.service)
}

func (s *runServer) setPolicy(w http.ResponseWriter, r *http.Request) {
	asked := readBody[runv2.GoogleIamV1SetIamPolicyRequest](w, r)
	if asked == nil {
		return
	}
	s.policies = append(s.policies, asked.Policy)
	writeBody(w, asked.Policy)
}

func conflicted(w http.ResponseWriter, said string) {
	w.WriteHeader(http.StatusConflict)
	w.Write([]byte(`{"error":{"code":409,"message":"` + said + `"}}`))
}

func readBody[T any](w http.ResponseWriter, r *http.Request) *T {
	var body T
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return nil
	}
	return &body
}

func writeBody(w http.ResponseWriter, body any) {
	if err := json.NewEncoder(w).Encode(body); err != nil {
		panic(err)
	}
}

func (s *runServer) bound() []*runv2.GoogleIamV1Policy {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.policies)
}

func (s *runServer) current() *runv2.GoogleCloudRunV2Service {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.created) == 0 {
		return &runv2.GoogleCloudRunV2Service{}
	}
	return s.created[len(s.created)-1]
}

func (s *runServer) serving() *runv2.GoogleCloudRunV2Service {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.service
}

func (s *runServer) releases() []*runv2.GoogleCloudRunV2Service {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.patched)
}

func (s *runServer) remaining() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.revisions)
}

func (s *runServer) tries() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.patchTries
}

func (s *runServer) store(t *testing.T, w http.ResponseWriter, r *http.Request) {
	t.Helper()
	_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		t.Errorf("an upload carried no content type: %v", err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	parts := multipart.NewReader(r.Body, params["boundary"])
	var body []byte
	var attrs struct {
		ContentType  string `json:"contentType"`
		CacheControl string `json:"cacheControl"`
	}
	for i := 0; ; i++ {
		part, err := parts.NextPart()
		if err != nil {
			break
		}
		switch i {
		case 0:
			_ = json.NewDecoder(part).Decode(&attrs)
		case 1:
			body, _ = io.ReadAll(part)
		}
	}
	query := r.URL.Query()
	name := query.Get("name")
	s.uploads = append(s.uploads, upload{
		name: name, ifGenerationMatch: query.Get("ifGenerationMatch"), body: body,
		contentType: attrs.ContentType, cacheControl: attrs.CacheControl,
	})
	s.events = append(s.events, "upload "+name)
	if query.Get("ifGenerationMatch") == "0" && s.present[name] {
		w.WriteHeader(http.StatusPreconditionFailed)
		w.Write([]byte(`{"error":{"code":412,"message":"conditionNotMet"}}`))
		return
	}
	writeBody(w, map[string]string{"bucket": "b", "name": name, "generation": "1"})
}

func (s *runServer) objectAttrs(w http.ResponseWriter, path string) {
	bucket, name, _ := strings.Cut(strings.TrimPrefix(path, "/storage/v1/b/"), "/o/")
	if !s.present[name] {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":{"code":404,"message":"No such object"}}`))
		return
	}
	writeBody(w, map[string]string{"bucket": bucket, "name": name, "generation": "1"})
}

func (s *runServer) stored() []upload {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.uploads)
}

func (s *runServer) happened() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.events)
}

func TestTheFakeRegistryListsNoTagItAnswersAsNotFound(t *testing.T) {
	gone := shopWebPackage + "/tags/sha256-gone"
	server := &runServer{
		tagged:  map[string][]string{shopWebPackage: {"sha256-kept", "sha256-gone"}},
		missing: []string{gone},
	}
	serve := server.serve(t)

	read := httptest.NewRecorder()
	serve(read, httptest.NewRequest(http.MethodGet, "/v1/"+gone, nil))
	if read.Code != http.StatusNotFound {
		t.Errorf("GET %s = %d, want %d for a tag a release pruned", gone, read.Code, http.StatusNotFound)
	}

	list := httptest.NewRecorder()
	serve(list, httptest.NewRequest(http.MethodGet, "/v1/"+shopWebPackage+"/tags", nil))
	var listed struct {
		Tags []struct{ Name string } `json:"tags"`
	}
	if err := json.NewDecoder(list.Body).Decode(&listed); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tag := range listed.Tags {
		names = append(names, tag.Name)
	}
	if want := []string{shopWebPackage + "/tags/sha256-kept"}; !slices.Equal(names, want) {
		t.Errorf("the tag listing = %v, want %v: a tag its GET answers 404 for is not listed either", names, want)
	}
}
