package webhook

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	"github.com/kelos-dev/kelos/internal/reporting"
	"github.com/kelos-dev/kelos/internal/taskbuilder"
)

// newGitLabTestHandler creates a WebhookHandler for the gitlab source backed
// by a fake client.
func newGitLabTestHandler(t *testing.T, objs ...client.Object) *WebhookHandler {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := kelos.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		WithStatusSubresource(&kelos.TaskSpawner{}).
		Build()

	tb, err := taskbuilder.NewTaskBuilder(fakeClient)
	if err != nil {
		t.Fatal(err)
	}

	return &WebhookHandler{
		client:        fakeClient,
		source:        GitLabSource,
		log:           logr.Discard(),
		taskBuilder:   tb,
		secret:        []byte(testSecret),
		deliveryCache: NewDeliveryCache(context.Background()),
	}
}

func newGitLabWebhookSpawner(name string, gitlabWebhook *kelos.GitLabWebhook) *kelos.TaskSpawner {
	return &kelos.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
			UID:       "gitlab-spawner-uid",
		},
		Spec: kelos.TaskSpawnerSpec{
			When: kelos.When{GitLabWebhook: gitlabWebhook},
			TaskTemplate: kelos.TaskTemplate{
				Type: "claude-code",
				Credentials: &kelos.Credentials{
					Type: "api-key",
				},
				WorkspaceRef: &kelos.WorkspaceReference{
					Name: "test-workspace",
				},
				PromptTemplate: "Review MR !{{.Number}}: {{.Title}} on {{.Branch}}",
			},
		},
	}
}

func postGitLabWebhook(handler *WebhookHandler, payload, token, deliveryID string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader([]byte(payload)))
	req.Header.Set(GitLabEventHeader, "Merge Request Hook")
	if token != "" {
		req.Header.Set(GitLabTokenHeader, token)
	}
	if deliveryID != "" {
		req.Header.Set(GitLabDeliveryHeader, deliveryID)
	}
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	return rr
}

func TestServeHTTP_GitLabRejectsInvalidToken(t *testing.T) {
	handler := newGitLabTestHandler(t)

	for _, token := range []string{"", "wrong-token"} {
		rr := postGitLabWebhook(handler, gitlabMergeRequestPayload, token, "uuid-1")
		if rr.Code != http.StatusUnauthorized {
			t.Errorf("token %q: expected %d, got %d", token, http.StatusUnauthorized, rr.Code)
		}
	}
}

func TestServeHTTP_GitLabCreatesTaskAndDeduplicatesDelivery(t *testing.T) {
	spawner := newGitLabWebhookSpawner("mr-spawner", &kelos.GitLabWebhook{
		Events:  []string{"merge_request"},
		Project: "group/subgroup/project",
	})
	handler := newGitLabTestHandler(t, spawner)

	rr := postGitLabWebhook(handler, gitlabMergeRequestPayload, testSecret, "uuid-42")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d: %s", http.StatusOK, rr.Code, rr.Body.String())
	}

	var taskList kelos.TaskList
	if err := handler.client.List(context.Background(), &taskList); err != nil {
		t.Fatal(err)
	}
	if len(taskList.Items) != 1 {
		t.Fatalf("expected 1 task, got %d", len(taskList.Items))
	}
	task := taskList.Items[0]
	if task.Spec.Prompt != "Review MR !41: Add feature on feature-branch" {
		t.Errorf("task prompt = %q", task.Spec.Prompt)
	}
	// No reporting configured — no reporting annotations stamped.
	if _, ok := task.Annotations[reporting.AnnotationGitLabReporting]; ok {
		t.Error("gitlab reporting annotation must not be set without reporting config")
	}

	// Same delivery UUID again — deduplicated, still one task.
	rr = postGitLabWebhook(handler, gitlabMergeRequestPayload, testSecret, "uuid-42")
	if rr.Code != http.StatusOK {
		t.Fatalf("duplicate delivery: expected %d, got %d", http.StatusOK, rr.Code)
	}
	if err := handler.client.List(context.Background(), &taskList); err != nil {
		t.Fatal(err)
	}
	if len(taskList.Items) != 1 {
		t.Errorf("expected still 1 task after duplicate delivery, got %d", len(taskList.Items))
	}
}

func TestServeHTTP_GitLabProjectMismatchCreatesNoTask(t *testing.T) {
	spawner := newGitLabWebhookSpawner("mr-spawner", &kelos.GitLabWebhook{
		Events:  []string{"merge_request"},
		Project: "other/project",
	})
	handler := newGitLabTestHandler(t, spawner)

	rr := postGitLabWebhook(handler, gitlabMergeRequestPayload, testSecret, "uuid-43")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d", http.StatusOK, rr.Code)
	}

	var taskList kelos.TaskList
	if err := handler.client.List(context.Background(), &taskList); err != nil {
		t.Fatal(err)
	}
	if len(taskList.Items) != 0 {
		t.Errorf("expected no tasks for project mismatch, got %d", len(taskList.Items))
	}
}

func TestServeHTTP_GitLabStampsReportingAnnotations(t *testing.T) {
	spawner := newGitLabWebhookSpawner("mr-spawner", &kelos.GitLabWebhook{
		Events: []string{"merge_request"},
		Reporting: &kelos.GitLabReporting{
			Comments: &kelos.GitLabCommentsReporting{Mode: kelos.GitLabCommentModeSticky},
		},
	})
	handler := newGitLabTestHandler(t, spawner)

	rr := postGitLabWebhook(handler, gitlabMergeRequestPayload, testSecret, "uuid-44")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d", http.StatusOK, rr.Code)
	}

	var taskList kelos.TaskList
	if err := handler.client.List(context.Background(), &taskList); err != nil {
		t.Fatal(err)
	}
	if len(taskList.Items) != 1 {
		t.Fatalf("expected 1 task, got %d", len(taskList.Items))
	}
	annotations := taskList.Items[0].Annotations
	expected := map[string]string{
		reporting.AnnotationGitLabReporting:   "enabled",
		reporting.AnnotationGitLabCommentMode: string(kelos.GitLabCommentModeSticky),
		reporting.AnnotationSourceKind:        "merge-request",
		reporting.AnnotationSourceNumber:      "41",
		reporting.AnnotationSourceProject:     "group/subgroup/project",
	}
	for key, want := range expected {
		if got := annotations[key]; got != want {
			t.Errorf("annotation %s = %q, want %q", key, got, want)
		}
	}
	if _, ok := annotations[reporting.AnnotationGitHubReporting]; ok {
		t.Error("GitHub reporting annotation must not be set for GitLab webhooks")
	}
}

func TestServeHTTP_GitLabMissingDeliveryHeaderDeduplicatesByBody(t *testing.T) {
	spawner := newGitLabWebhookSpawner("mr-spawner", &kelos.GitLabWebhook{
		Events: []string{"merge_request"},
	})
	handler := newGitLabTestHandler(t, spawner)

	for i := 0; i < 2; i++ {
		rr := postGitLabWebhook(handler, gitlabMergeRequestPayload, testSecret, "")
		if rr.Code != http.StatusOK {
			t.Fatalf("request %d: expected %d, got %d", i, http.StatusOK, rr.Code)
		}
	}

	var taskList kelos.TaskList
	if err := handler.client.List(context.Background(), &taskList); err != nil {
		t.Fatal(err)
	}
	if len(taskList.Items) != 1 {
		t.Errorf("expected 1 task for byte-identical retries, got %d", len(taskList.Items))
	}
}
