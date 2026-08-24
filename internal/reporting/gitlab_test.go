package reporting

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

const gitlabTestProject = "group/subgroup/project"

type gitlabNoteRecord struct {
	method string
	path   string
	id     int64
	body   string
}

// newGitLabTestServer fakes the GitLab notes API for one project. It records
// note create/update calls and asserts the project path arrives URL-encoded.
func newGitLabTestServer(t *testing.T) (*httptest.Server, *[]gitlabNoteRecord) {
	t.Helper()
	var (
		mu      sync.Mutex
		records []gitlabNoteRecord
		nextID  int64 = 5000
		notes         = map[int64]string{}
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()

		if r.URL.Path == "/user" {
			json.NewEncoder(w).Encode(gitlabNoteUser{Username: "reporter"})
			return
		}

		if !strings.HasPrefix(r.URL.EscapedPath(), "/projects/"+url.PathEscape(gitlabTestProject)+"/") {
			t.Errorf("request path %q does not carry the escaped project path", r.URL.EscapedPath())
		}

		var body gitlabNoteRequest
		json.NewDecoder(r.Body).Decode(&body)

		switch r.Method {
		case http.MethodGet:
			response := make([]gitlabNoteResponse, 0, len(notes))
			for id, noteBody := range notes {
				response = append(response, gitlabNoteResponse{
					ID: id, Body: noteBody, Author: gitlabNoteUser{Username: "reporter"},
				})
			}
			json.NewEncoder(w).Encode(response)
		case http.MethodPost:
			nextID++
			notes[nextID] = body.Body
			records = append(records, gitlabNoteRecord{method: "create", path: r.URL.Path, id: nextID, body: body.Body})
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(gitlabNoteResponse{ID: nextID})
		case http.MethodPut:
			segments := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
			id, _ := strconv.ParseInt(segments[len(segments)-1], 10, 64)
			notes[id] = body.Body
			records = append(records, gitlabNoteRecord{method: "update", path: r.URL.Path, id: id, body: body.Body})
			json.NewEncoder(w).Encode(gitlabNoteResponse{ID: id})
		}
	}))

	return server, &records
}

func TestGitLabReporterCreateAndUpdateNote(t *testing.T) {
	server, records := newGitLabTestServer(t)
	defer server.Close()

	reporter := &GitLabReporter{
		Project:  gitlabTestProject,
		ItemKind: "merge_requests",
		Token:    "glpat-test",
		BaseURL:  server.URL,
	}

	id, err := reporter.CreateComment(context.Background(), 41, "status: accepted")
	if err != nil {
		t.Fatalf("CreateComment() error = %v", err)
	}
	if err := reporter.UpdateComment(context.Background(), 41, id, "status: succeeded"); err != nil {
		t.Fatalf("UpdateComment() error = %v", err)
	}

	recs := *records
	if len(recs) != 2 {
		t.Fatalf("expected 2 note calls, got %d: %+v", len(recs), recs)
	}
	if !strings.HasSuffix(recs[0].path, "/merge_requests/41/notes") {
		t.Errorf("create path = %q, want .../merge_requests/41/notes", recs[0].path)
	}
	wantUpdate := "/merge_requests/41/notes/" + strconv.FormatInt(id, 10)
	if !strings.HasSuffix(recs[1].path, wantUpdate) {
		t.Errorf("update path = %q, want suffix %q", recs[1].path, wantUpdate)
	}
	if recs[1].body != "status: succeeded" {
		t.Errorf("update body = %q", recs[1].body)
	}
}

func TestGitLabReporterFindCommentByMarkerOwnership(t *testing.T) {
	marker := "<!-- kelos.dev/gitlab-status-comment:default/spawner -->"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/user" {
			json.NewEncoder(w).Encode(gitlabNoteUser{Username: "reporter"})
			return
		}
		json.NewEncoder(w).Encode([]gitlabNoteResponse{
			{ID: 1, Body: "someone quoting " + marker, Author: gitlabNoteUser{Username: "impostor"}},
			{ID: 2, Body: "status " + marker, Author: gitlabNoteUser{Username: "reporter"}},
			{ID: 3, Body: "unrelated note", Author: gitlabNoteUser{Username: "reporter"}},
		})
	}))
	defer server.Close()

	reporter := &GitLabReporter{Project: gitlabTestProject, ItemKind: "issues", Token: "glpat-test", BaseURL: server.URL}
	id, err := reporter.FindCommentByMarker(context.Background(), 7, marker)
	if err != nil {
		t.Fatalf("FindCommentByMarker() error = %v", err)
	}
	if id != 2 {
		t.Errorf("FindCommentByMarker() = %d, want 2 (impostor note must not match)", id)
	}
}

func TestReportTaskStatus_GitLabAnnotations(t *testing.T) {
	server, records := newGitLabTestServer(t)
	defer server.Close()

	task := newTaskWithAnnotations("gitlab-task", "default", kelos.TaskPhasePending, map[string]string{
		AnnotationGitLabReporting:   "enabled",
		AnnotationGitLabCommentMode: string(kelos.GitLabCommentModePerTask),
		AnnotationSourceKind:        "merge-request",
		AnnotationSourceNumber:      "41",
	})

	cl := fake.NewClientBuilder().WithScheme(newTestScheme()).WithObjects(task).Build()
	tr := &TaskReporter{
		Client: cl,
		Reporter: &GitLabReporter{
			Project:  gitlabTestProject,
			ItemKind: "merge_requests",
			Token:    "glpat-test",
			BaseURL:  server.URL,
		},
		CommentAnnotations: GitLabCommentAnnotations,
	}

	if err := tr.ReportTaskStatus(context.Background(), task); err != nil {
		t.Fatalf("ReportTaskStatus() error = %v", err)
	}

	recs := *records
	if len(recs) != 1 || recs[0].method != "create" {
		t.Fatalf("expected one note create, got %+v", recs)
	}
	if !strings.Contains(recs[0].body, "accepted") {
		t.Errorf("note body = %q, want accepted status", recs[0].body)
	}

	var persisted kelos.Task
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(task), &persisted); err != nil {
		t.Fatalf("getting task: %v", err)
	}
	if got := persisted.Annotations[AnnotationGitLabReportPhase]; got != "accepted" {
		t.Errorf("%s = %q, want accepted", AnnotationGitLabReportPhase, got)
	}
	noteID := persisted.Annotations[AnnotationGitLabCommentID]
	if noteID == "" || noteID == "0" {
		t.Fatalf("%s = %q, want the created note ID", AnnotationGitLabCommentID, noteID)
	}
	if _, ok := persisted.Annotations[AnnotationGitHubCommentID]; ok {
		t.Error("GitHub comment annotation must not be written for GitLab reporting")
	}

	// Phase transition updates the same note via the MR-scoped notes URL. The
	// reporter reads the phase from the passed object, so mutating it in
	// memory is sufficient.
	persisted.Status.Phase = kelos.TaskPhaseSucceeded
	if err := tr.ReportTaskStatus(context.Background(), &persisted); err != nil {
		t.Fatalf("ReportTaskStatus() error = %v", err)
	}

	recs = *records
	if len(recs) != 2 || recs[1].method != "update" {
		t.Fatalf("expected a note update, got %+v", recs)
	}
	if want := "/merge_requests/41/notes/" + noteID; !strings.HasSuffix(recs[1].path, want) {
		t.Errorf("update path = %q, want suffix %q", recs[1].path, want)
	}
	if !strings.Contains(recs[1].body, "succeeded") {
		t.Errorf("update body = %q, want succeeded status", recs[1].body)
	}
}

func TestReportTaskStatus_GitLabSkipsWithoutGitLabAnnotation(t *testing.T) {
	server, records := newGitLabTestServer(t)
	defer server.Close()

	// GitHub-annotated task must be ignored by a GitLab-configured reporter.
	task := newTaskWithAnnotations("github-task", "default", kelos.TaskPhasePending, map[string]string{
		AnnotationGitHubReporting: "enabled",
		AnnotationSourceNumber:    "7",
	})

	cl := fake.NewClientBuilder().WithScheme(newTestScheme()).WithObjects(task).Build()
	tr := &TaskReporter{
		Client:             cl,
		Reporter:           &GitLabReporter{Project: gitlabTestProject, ItemKind: "issues", BaseURL: server.URL},
		CommentAnnotations: GitLabCommentAnnotations,
	}

	if err := tr.ReportTaskStatus(context.Background(), task); err != nil {
		t.Fatalf("ReportTaskStatus() error = %v", err)
	}
	if len(*records) != 0 {
		t.Errorf("expected no GitLab API calls, got %+v", *records)
	}
}

func TestReportTaskStatus_GitLabStickyNoteReused(t *testing.T) {
	server, records := newGitLabTestServer(t)
	defer server.Close()

	annotations := func() map[string]string {
		return map[string]string{
			AnnotationGitLabReporting:   "enabled",
			AnnotationGitLabCommentMode: string(kelos.GitLabCommentModeSticky),
			AnnotationSourceKind:        "issue",
			AnnotationSourceNumber:      "7",
		}
	}
	first := newTaskWithAnnotations("task-a", "default", kelos.TaskPhasePending, annotations())
	first.Labels = map[string]string{"kelos.dev/taskspawner": "spawner"}
	second := newTaskWithAnnotations("task-b", "default", kelos.TaskPhasePending, annotations())
	second.Labels = map[string]string{"kelos.dev/taskspawner": "spawner"}

	cl := fake.NewClientBuilder().WithScheme(newTestScheme()).WithObjects(first, second).Build()
	tr := &TaskReporter{
		Client: cl,
		Reporter: &GitLabReporter{
			Project:  gitlabTestProject,
			ItemKind: "issues",
			Token:    "glpat-test",
			BaseURL:  server.URL,
		},
		CommentAnnotations: GitLabCommentAnnotations,
	}

	if err := tr.ReportTaskStatus(context.Background(), first); err != nil {
		t.Fatalf("ReportTaskStatus(first) error = %v", err)
	}
	if err := tr.ReportTaskStatus(context.Background(), second); err != nil {
		t.Fatalf("ReportTaskStatus(second) error = %v", err)
	}

	recs := *records
	if len(recs) != 2 {
		t.Fatalf("expected 2 note calls, got %+v", recs)
	}
	if recs[0].method != "create" || recs[1].method != "update" {
		t.Fatalf("expected create then update of the sticky note, got %+v", recs)
	}
	if recs[1].id != recs[0].id {
		t.Errorf("second task updated note %d, want reuse of %d", recs[1].id, recs[0].id)
	}
	if !strings.Contains(recs[0].body, "kelos.dev/gitlab-status-comment:default/spawner") {
		t.Errorf("sticky note body missing GitLab marker: %q", recs[0].body)
	}
}
