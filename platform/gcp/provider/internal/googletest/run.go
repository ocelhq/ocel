package googletest

import (
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
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

type Run struct {
	Mu        sync.Mutex
	Service   *runv2.GoogleCloudRunV2Service
	Revision  int
	revisions []string
	writes    int

	Created  []*runv2.GoogleCloudRunV2Service
	Patched  []*runv2.GoogleCloudRunV2Service
	policies []*runv2.GoogleIamV1Policy

	PatchConflicts int
	OnConflict     func(*runv2.GoogleCloudRunV2Service)
	patchTries     int

	UriEachRevision bool
	FailRouting     bool
	RefuseReads     bool
	OnCreate        func()

	uploads []Upload
	Present map[string]bool
	events  []string

	Iam     *IAM
	Secrets *Secrets

	templates map[string]*runv2.GoogleCloudRunV2RevisionTemplate
	Elsewhere map[string]string
	untagged  []string
	regional  int
	asked     []string
	Missing   []string
	retagged  []string
	Versions  map[string]string
	OnUntag   func(tag string)

	UntagAtRelease    string
	UntagEveryRelease bool

	ReportsDigests         bool
	StripsRevisionMetadata bool
	ReleasedUnder          *runv2.GoogleCloudRunV2RevisionTemplate

	Tagged map[string][]string
}

type Upload struct {
	Name, IfGenerationMatch string
	Body                    []byte
	ContentType             string
	CacheControl            string
}

func (s *Run) Identities() *IAM {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	if s.Iam == nil {
		s.Iam = GrantedIAM()
	}
	return s.Iam
}

func (s *Run) Serve(t *testing.T) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		s.Mu.Lock()
		defer s.Mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		path := r.URL.Path
		if s.Iam == nil {
			s.Iam = GrantedIAM()
		}
		switch {
		case strings.HasPrefix(path, "/v1/projects/") && strings.Contains(path, "/secrets"):
			if s.Secrets == nil {
				s.Secrets = NewSecrets()
			}
			s.Secrets.Serve(t, w, r)
		case strings.HasPrefix(path, "/v1/") && !strings.Contains(path, "/packages/"),
			strings.HasPrefix(path, "/storage/v1/b/") && strings.HasSuffix(path, "/iam"):
			s.Iam.Rest(t)(w, r)
		case r.Method == http.MethodPost && strings.HasPrefix(path, "/upload/storage/v1/b/"):
			s.store(t, w, r)
		case r.Method == http.MethodPost && strings.HasSuffix(path, ":setIamPolicy"):
			s.setPolicy(w, r)
		case r.Method == http.MethodGet && strings.HasSuffix(path, ":getIamPolicy"):
			WriteBody(w, &runv2.GoogleIamV1Policy{Etag: "BwXhoLA="})
		case r.Method == http.MethodPost && strings.HasSuffix(path, "/services"):
			s.create(w, r)
		case r.Method == http.MethodPatch:
			s.patch(w, r)
		case r.Method == http.MethodDelete && strings.Contains(path, "/packages/") && strings.Contains(path, "/tags/"):
			s.untagged = append(s.untagged, strings.TrimPrefix(r.URL.EscapedPath(), "/v1/"))
			if s.OnUntag != nil {
				s.OnUntag(strings.TrimPrefix(r.URL.EscapedPath(), "/v1/"))
			}
			WriteBody(w, map[string]any{})
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
			s.Service = nil
			s.revisions = nil
			WriteBody(w, &runv2.GoogleLongrunningOperation{Name: "operations/delete", Done: true})
		case r.Method == http.MethodGet && strings.Contains(path, "/operations/"):
			WriteBody(w, &runv2.GoogleLongrunningOperation{Name: strings.TrimPrefix(path, "/v2/"), Done: true})
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

func (s *Run) create(w http.ResponseWriter, r *http.Request) {
	desired := ReadBody[runv2.GoogleCloudRunV2Service](w, r)
	if desired == nil {
		return
	}
	if s.OnCreate != nil {
		s.OnCreate()
	}
	s.Created = append(s.Created, desired)
	s.events = append(s.events, "create")
	name := r.URL.Query().Get("serviceId")
	s.Service = &runv2.GoogleCloudRunV2Service{
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
		WriteBody(w, failed)
		return
	}
	s.revised()
	WriteBody(w, &runv2.GoogleLongrunningOperation{Name: "operations/create", Done: true})
}

func (s *Run) patch(w http.ResponseWriter, r *http.Request) {
	desired := ReadBody[runv2.GoogleCloudRunV2Service](w, r)
	if desired == nil {
		return
	}
	s.patchTries++
	if desired.Etag != "" && desired.Etag != s.Service.Etag {
		Conflicted(w, "the service's etag is "+s.Service.Etag+", not the "+desired.Etag+" this write was based on")
		return
	}
	if s.PatchConflicts > 0 {
		s.PatchConflicts--
		if s.OnConflict != nil {
			s.OnConflict(s.Service)
		}
		Conflicted(w, "the service was changed under this release")
		return
	}
	s.Patched = append(s.Patched, desired)
	if mask := r.URL.Query().Get("updateMask"); mask != "" {
		fields := strings.Split(mask, ",")
		if slices.Contains(fields, "traffic") {
			s.Service.Traffic = allocated(desired.Traffic)
			s.Service.TrafficStatuses = taggedStatuses(s.Service)
			s.served()
		}
		if slices.Contains(fields, "invoker_iam_disabled") {
			s.Service.InvokerIamDisabled = desired.InvokerIamDisabled
		}
		if slices.Contains(fields, "annotations") {
			s.Service.Annotations = desired.Annotations
		}
		s.writes++
		s.Service.Etag = "etag-" + strconv.Itoa(s.writes)
		if s.FailRouting {
			WriteBody(w, &runv2.GoogleLongrunningOperation{Name: "operations/release", Done: true, Error: &runv2.GoogleRpcStatus{Message: "the operation failed"}})
			return
		}
		WriteBody(w, &runv2.GoogleLongrunningOperation{Name: "operations/release", Done: true})
		return
	}
	replaced := !SameTemplate(s.Service.Template, desired.Template)
	s.Service.Template = desired.Template
	if failed := s.untagUnderRelease(desired.Template); failed != nil {
		WriteBody(w, failed)
		return
	}
	s.Service.Traffic = allocated(desired.Traffic)
	s.Service.Ingress = desired.Ingress
	s.Service.InvokerIamDisabled = desired.InvokerIamDisabled
	s.Service.Labels = desired.Labels
	s.Service.CustomAudiences = desired.CustomAudiences
	s.Service.Annotations = desired.Annotations
	s.writes++
	s.Service.Etag = "etag-" + strconv.Itoa(s.writes)
	if replaced {
		s.revised()
	} else {
		s.served()
	}
	WriteBody(w, &runv2.GoogleLongrunningOperation{Name: "operations/release", Done: true})
}

func (s *Run) untagUnderRelease(template *runv2.GoogleCloudRunV2RevisionTemplate) *runv2.GoogleLongrunningOperation {
	if s.UntagAtRelease == "" {
		return nil
	}
	s.Missing = append(s.Missing, s.UntagAtRelease)
	if !s.UntagEveryRelease {
		s.UntagAtRelease = ""
	}
	return &runv2.GoogleLongrunningOperation{Name: "operations/release", Done: true, Error: &runv2.GoogleRpcStatus{
		Code:    9,
		Message: "Revision '" + run.RevisionName(s.Service.Name) + "-failed' is not ready and cannot serve traffic. Image '" + template.Containers[0].Image + "' not found.",
	}}
}

func (s *Run) revised() {
	s.Revision++
	s.writes++
	s.Service.LatestCreatedRevision = s.Service.Name + "/revisions/" + run.RevisionName(s.Service.Name) + "-0000" + strconv.Itoa(s.Revision)
	created := run.RevisionName(s.Service.LatestCreatedRevision)
	if s.UriEachRevision {
		s.Service.Uri = "https://" + run.RevisionName(s.Service.Name) + "-" + strconv.Itoa(s.Revision) + ".run.app"
	}
	s.Service.Etag = "etag-" + strconv.Itoa(s.writes)
	s.revisions = append(s.revisions, created)
	if s.templates == nil {
		s.templates = map[string]*runv2.GoogleCloudRunV2RevisionTemplate{}
	}
	if s.Service.Template != nil {
		s.templates[created] = s.Service.Template
	}
	s.served()
	if under := s.ReleasedUnder; under != nil {
		s.ReleasedUnder = nil
		s.Service.Template = under
		s.revised()
	}
}

func (s *Run) served() {
	newest := slices.Index(s.revisions, run.RevisionName(s.Service.LatestReadyRevision))
	for _, target := range s.Service.Traffic {
		if target.Percent == 0 {
			continue
		}
		revision := run.RevisionName(target.Revision)
		if target.Type == run.TrafficByLatest {
			revision = run.RevisionName(s.Service.LatestCreatedRevision)
		}
		newest = max(newest, slices.Index(s.revisions, revision))
	}
	if newest >= 0 {
		s.Service.LatestReadyRevision = s.Service.Name + "/revisions/" + s.revisions[newest]
	}
}

func (s *Run) listLabelled(w http.ResponseWriter, selector string) {
	s.asked = append(s.asked, selector)
	key, value, _ := strings.Cut(selector, "=")
	listed := &runv1.ListRevisionsResponse{}
	if s.Service != nil {
		for _, name := range s.revisions {
			if s.templates[name] != nil && s.templates[name].Labels[key] == value {
				listed.Items = append(listed.Items, &runv1.Revision{Metadata: &runv1.ObjectMeta{Name: name}})
			}
		}
	}
	for name, image := range s.Elsewhere {
		if run.ImageLabelValue(image) == value {
			listed.Items = append(listed.Items, &runv1.Revision{Metadata: &runv1.ObjectMeta{Name: name}})
		}
	}
	WriteBody(w, listed)
}

func (s *Run) getTag(w http.ResponseWriter, name string) {
	unescaped, _ := url.PathUnescape(name)
	if slices.Contains(s.Missing, unescaped) || slices.Contains(s.untagged, name) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":404,"message":"tag not found"}}`))
		return
	}
	WriteBody(w, map[string]any{"name": unescaped, "version": s.versionOf(unescaped)})
}

func (s *Run) versionOf(tag string) string {
	if version := s.Versions[tag]; version != "" {
		return version
	}
	at := strings.LastIndex(tag, "/tags/")
	return tag[:at] + "/versions/sha256:" + strings.TrimPrefix(tag[at+len("/tags/"):], "sha256-")
}

func (s *Run) reportedImage(image string) string {
	at := strings.LastIndex(image, ":")
	if !s.ReportsDigests || strings.Contains(image, "@") || at < strings.LastIndex(image, "/") {
		return image
	}
	digest := "sha256:" + strings.TrimPrefix(image[at+1:], "sha256-")
	if name, tagged := run.ArtifactTag(image); tagged {
		unescaped, _ := url.PathUnescape(name)
		_, digest, _ = strings.Cut(s.versionOf(unescaped), "/versions/")
	}
	return image[:at] + "@" + digest
}

func (s *Run) listTags(w http.ResponseWriter, parent string) {
	listed := map[string]any{}
	var tags []map[string]string
	for _, tag := range s.Tagged[strings.TrimSuffix(parent, "/tags")] {
		name := strings.TrimSuffix(parent, "/tags") + "/tags/" + tag
		unescaped, _ := url.PathUnescape(name)
		if !slices.Contains(s.Missing, unescaped) && !slices.Contains(s.untagged, name) {
			tags = append(tags, map[string]string{"name": name})
		}
	}
	listed["tags"] = tags
	WriteBody(w, listed)
}

func (s *Run) createTag(w http.ResponseWriter, r *http.Request) {
	tag := ReadBody[map[string]any](w, r)
	if tag == nil {
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/v1/") + "/" + r.URL.Query().Get("tagId")
	s.retagged = append(s.retagged, name+" -> "+fmt.Sprint((*tag)["version"]))
	s.Missing = slices.DeleteFunc(s.Missing, func(missing string) bool { return missing == name })
	WriteBody(w, tag)
}

func (s *Run) LabelQueries() []string {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	return slices.Clone(s.asked)
}

func (s *Run) Retags() []string {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	return slices.Clone(s.retagged)
}

func (s *Run) revisionOf(name string) *runv2.GoogleCloudRunV2Revision {
	revision := revisionFrom(s.Service.Name+"/revisions/"+name, s.templates[name])
	if s.StripsRevisionMetadata {
		revision.Labels, revision.Annotations = nil, nil
	}
	for _, container := range revision.Containers {
		container.Image = s.reportedImage(container.Image)
	}
	return revision
}

func revisionFrom(name string, template *runv2.GoogleCloudRunV2RevisionTemplate) *runv2.GoogleCloudRunV2Revision {
	revision := &runv2.GoogleCloudRunV2Revision{
		Name:       name,
		Conditions: []*runv2.GoogleCloudRunV2Condition{{Type: run.ReadyCondition, State: run.ConditionSucceeded}},
	}
	if template == nil {
		return revision
	}
	revision.Labels, revision.Annotations, revision.ServiceAccount = template.Labels, template.Annotations, template.ServiceAccount
	for _, container := range template.Containers {
		revision.Containers = append(revision.Containers, &runv2.GoogleCloudRunV2Container{Image: container.Image})
	}
	return revision
}

func (s *Run) getRevision(w http.ResponseWriter, name string) {
	if s.Service == nil || !slices.Contains(s.revisions, name) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":404,"message":"revision not found"}}`))
		return
	}
	WriteBody(w, s.revisionOf(name))
}

func (s *Run) listRevisions(w http.ResponseWriter) {
	listed := &runv2.GoogleCloudRunV2ListRevisionsResponse{}
	for _, name := range s.revisions {
		listed.Revisions = append(listed.Revisions, s.revisionOf(name))
	}
	WriteBody(w, listed)
}

func (s *Run) listRegionRevisions(w http.ResponseWriter) {
	s.regional++
	listed := &runv2.GoogleCloudRunV2ListRevisionsResponse{}
	if s.Service != nil {
		for _, name := range s.revisions {
			listed.Revisions = append(listed.Revisions, s.revisionOf(name))
		}
	}
	for name, image := range s.Elsewhere {
		listed.Revisions = append(listed.Revisions, &runv2.GoogleCloudRunV2Revision{
			Name:       "projects/acme/locations/europe-west1/services/elsewhere/revisions/" + name,
			Containers: []*runv2.GoogleCloudRunV2Container{{Image: image}},
		})
	}
	WriteBody(w, listed)
}

func (s *Run) RegionListings() int {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	return s.regional
}

func (s *Run) Untags() []string {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	return slices.Clone(s.untagged)
}

func (s *Run) deleteRevision(w http.ResponseWriter, revision string) {
	if s.Service == nil || !slices.Contains(s.revisions, revision) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":404,"message":"revision not found"}}`))
		return
	}
	routed := slices.ContainsFunc(s.Service.Traffic, func(target *runv2.GoogleCloudRunV2TrafficTarget) bool {
		return run.RevisionName(target.Revision) == revision ||
			target.Type == run.TrafficByLatest && run.RevisionName(s.Service.LatestReadyRevision) == revision
	})
	if routed || run.RevisionName(s.Service.LatestReadyRevision) == revision {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":400,"status":"FAILED_PRECONDITION","message":"revision ` + revision + ` is the latest or can receive traffic"}}`))
		return
	}
	s.revisions = slices.DeleteFunc(s.revisions, func(name string) bool { return name == revision })
	WriteBody(w, &runv2.GoogleLongrunningOperation{Name: "operations/delete-revision", Done: true})
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

func SameTemplate(current, desired *runv2.GoogleCloudRunV2RevisionTemplate) bool {
	was, _ := json.Marshal(current)
	is, _ := json.Marshal(desired)
	return string(was) == string(is)
}

func (s *Run) get(w http.ResponseWriter) {
	if s.RefuseReads {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"code":403,"message":"permission denied on the service"}}`))
		return
	}
	if s.Service == nil {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":404,"message":"service not found"}}`))
		return
	}
	WriteBody(w, s.Service)
}

func (s *Run) setPolicy(w http.ResponseWriter, r *http.Request) {
	asked := ReadBody[runv2.GoogleIamV1SetIamPolicyRequest](w, r)
	if asked == nil {
		return
	}
	s.policies = append(s.policies, asked.Policy)
	WriteBody(w, asked.Policy)
}

func Conflicted(w http.ResponseWriter, said string) {
	w.WriteHeader(http.StatusConflict)
	_, _ = w.Write([]byte(`{"error":{"code":409,"message":"` + said + `"}}`))
}

func (s *Run) Bound() []*runv2.GoogleIamV1Policy {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	return slices.Clone(s.policies)
}

func (s *Run) Current() *runv2.GoogleCloudRunV2Service {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	if len(s.Created) == 0 {
		return &runv2.GoogleCloudRunV2Service{}
	}
	return s.Created[len(s.Created)-1]
}

func (s *Run) Serving() *runv2.GoogleCloudRunV2Service {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	return s.Service
}

func (s *Run) Releases() []*runv2.GoogleCloudRunV2Service {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	return slices.Clone(s.Patched)
}

func (s *Run) Remaining() []string {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	return slices.Clone(s.revisions)
}

func (s *Run) Tries() int {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	return s.patchTries
}

func (s *Run) store(t *testing.T, w http.ResponseWriter, r *http.Request) {
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
	s.uploads = append(s.uploads, Upload{
		Name: name, IfGenerationMatch: query.Get("ifGenerationMatch"), Body: body,
		ContentType: attrs.ContentType, CacheControl: attrs.CacheControl,
	})
	s.events = append(s.events, "upload "+name)
	if query.Get("ifGenerationMatch") == "0" && s.Present[name] {
		w.WriteHeader(http.StatusPreconditionFailed)
		_, _ = w.Write([]byte(`{"error":{"code":412,"message":"conditionNotMet"}}`))
		return
	}
	WriteBody(w, map[string]string{"bucket": "b", "name": name, "generation": "1"})
}

func (s *Run) objectAttrs(w http.ResponseWriter, path string) {
	bucket, name, _ := strings.Cut(strings.TrimPrefix(path, "/storage/v1/b/"), "/o/")
	if !s.Present[name] {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":404,"message":"No such object"}}`))
		return
	}
	WriteBody(w, map[string]string{"bucket": bucket, "name": name, "generation": "1"})
}

func (s *Run) Stored() []Upload {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	return slices.Clone(s.uploads)
}

func (s *Run) Happened() []string {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	return slices.Clone(s.events)
}
