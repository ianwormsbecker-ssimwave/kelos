package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	"github.com/kelos-dev/kelos/internal/reporting"
)

func TestReportingAnnotationPredicate_Create(t *testing.T) {
	tests := []struct {
		name        string
		annotations map[string]string
		want        bool
	}{
		{name: "reporting enabled", annotations: map[string]string{reporting.AnnotationGitHubReporting: "enabled"}, want: true},
		{name: "checks enabled", annotations: map[string]string{reporting.AnnotationGitHubChecks: "enabled"}, want: true},
		{name: "both enabled", annotations: map[string]string{reporting.AnnotationGitHubReporting: "enabled", reporting.AnnotationGitHubChecks: "enabled"}, want: true},
		{name: "reporting disabled value", annotations: map[string]string{reporting.AnnotationGitHubReporting: "disabled"}, want: false},
		{name: "gitlab reporting enabled", annotations: map[string]string{reporting.AnnotationGitLabReporting: "enabled"}, want: true},
		{name: "gitlab reporting disabled value", annotations: map[string]string{reporting.AnnotationGitLabReporting: "disabled"}, want: false},
		{name: "missing annotation", annotations: nil, want: false},
		{name: "unrelated annotations only", annotations: map[string]string{"other": "value"}, want: false},
	}

	pred := reportingAnnotationPredicate{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			task := &kelos.Task{ObjectMeta: metav1.ObjectMeta{Annotations: tt.annotations}}
			if got := pred.Create(event.CreateEvent{Object: task}); got != tt.want {
				t.Errorf("Create(%v) = %v, want %v", tt.annotations, got, tt.want)
			}
		})
	}
}

func TestReportingAnnotationPredicate_Update(t *testing.T) {
	tests := []struct {
		name        string
		annotations map[string]string
		oldPhase    kelos.TaskPhase
		newPhase    kelos.TaskPhase
		want        bool
	}{
		{
			name:        "enabled, phase changed",
			annotations: map[string]string{reporting.AnnotationGitHubReporting: "enabled"},
			oldPhase:    kelos.TaskPhasePending,
			newPhase:    kelos.TaskPhaseRunning,
			want:        true,
		},
		{
			name:        "enabled, phase unchanged",
			annotations: map[string]string{reporting.AnnotationGitHubReporting: "enabled"},
			oldPhase:    kelos.TaskPhaseRunning,
			newPhase:    kelos.TaskPhaseRunning,
			want:        false,
		},
		{
			name:        "checks only, phase changed",
			annotations: map[string]string{reporting.AnnotationGitHubChecks: "enabled"},
			oldPhase:    kelos.TaskPhasePending,
			newPhase:    kelos.TaskPhaseRunning,
			want:        true,
		},
		{
			name:        "missing annotation, phase changed",
			annotations: nil,
			oldPhase:    kelos.TaskPhasePending,
			newPhase:    kelos.TaskPhaseSucceeded,
			want:        false,
		},
	}

	pred := reportingAnnotationPredicate{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oldTask := &kelos.Task{
				ObjectMeta: metav1.ObjectMeta{Annotations: tt.annotations},
				Status:     kelos.TaskStatus{Phase: tt.oldPhase},
			}
			newTask := &kelos.Task{
				ObjectMeta: metav1.ObjectMeta{Annotations: tt.annotations},
				Status:     kelos.TaskStatus{Phase: tt.newPhase},
			}
			if got := pred.Update(event.UpdateEvent{ObjectOld: oldTask, ObjectNew: newTask}); got != tt.want {
				t.Errorf("Update() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestReconcileGitLab_PostsNoteFromAnnotations(t *testing.T) {
	var notePaths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("unexpected %s request to %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		notePaths = append(notePaths, r.URL.EscapedPath())
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"id": 9001}`)
	}))
	defer server.Close()

	task := &kelos.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "gitlab-task",
			Namespace: "default",
			Annotations: map[string]string{
				reporting.AnnotationGitLabReporting:   "enabled",
				reporting.AnnotationGitLabCommentMode: string(kelos.GitLabCommentModePerTask),
				reporting.AnnotationSourceKind:        "merge-request",
				reporting.AnnotationSourceNumber:      "41",
				reporting.AnnotationSourceProject:     "group/subgroup/project",
			},
		},
		Spec: kelos.TaskSpec{
			Type:   "claude-code",
			Prompt: "test",
			Credentials: &kelos.Credentials{
				Type:      kelos.CredentialTypeAPIKey,
				SecretRef: &kelos.SecretReference{Name: "creds"},
			},
		},
		Status: kelos.TaskStatus{Phase: kelos.TaskPhaseRunning},
	}

	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(kelos.AddToScheme(scheme))
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(task).Build()

	r := &reportingReconciler{
		Client: cl,
		config: reportingConfig{
			GitLabToken:      "glpat-test",
			GitLabAPIBaseURL: server.URL,
		},
		cache: reporting.NewReportStateCache(),
	}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "gitlab-task", Namespace: "default"},
	}); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	if len(notePaths) != 1 {
		t.Fatalf("expected 1 note create, got %d: %v", len(notePaths), notePaths)
	}
	want := "/projects/" + url.PathEscape("group/subgroup/project") + "/merge_requests/41/notes"
	if notePaths[0] != want {
		t.Errorf("note path = %q, want %q", notePaths[0], want)
	}

	var persisted kelos.Task
	if err := cl.Get(context.Background(), types.NamespacedName{Name: "gitlab-task", Namespace: "default"}, &persisted); err != nil {
		t.Fatal(err)
	}
	if got := persisted.Annotations[reporting.AnnotationGitLabCommentID]; got != "9001" {
		t.Errorf("%s = %q, want 9001", reporting.AnnotationGitLabCommentID, got)
	}
	if got := persisted.Annotations[reporting.AnnotationGitLabReportPhase]; got != "accepted" {
		t.Errorf("%s = %q, want accepted", reporting.AnnotationGitLabReportPhase, got)
	}
}

func TestReconcileGitLab_SkipsWithoutProjectAnnotation(t *testing.T) {
	task := &kelos.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "gitlab-task",
			Namespace: "default",
			Annotations: map[string]string{
				reporting.AnnotationGitLabReporting: "enabled",
				reporting.AnnotationSourceNumber:    "41",
			},
		},
		Status: kelos.TaskStatus{Phase: kelos.TaskPhaseRunning},
	}

	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(kelos.AddToScheme(scheme))
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(task).Build()

	r := &reportingReconciler{
		Client: cl,
		config: reportingConfig{GitLabToken: "glpat-test"},
		cache:  reporting.NewReportStateCache(),
	}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "gitlab-task", Namespace: "default"},
	}); err != nil {
		t.Fatalf("Reconcile() error = %v; missing project must be a no-op", err)
	}
}
