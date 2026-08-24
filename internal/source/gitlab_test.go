package source

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

const gitlabTestProject = "group/subgroup/project"

// gitlabTestServer serves canned GitLab API responses keyed by escaped path.
// It asserts that every request carries the expected Bearer token and that
// the project path arrives URL-encoded as a single segment.
func gitlabTestServer(t *testing.T, token string, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if token != "" {
			if got := r.Header.Get("Authorization"); got != "Bearer "+token {
				t.Errorf("Authorization header = %q, want %q", got, "Bearer "+token)
			}
		}
		// The project path must be escaped into a single segment.
		if !strings.HasPrefix(r.URL.EscapedPath(), "/projects/"+url.PathEscape(gitlabTestProject)) {
			t.Errorf("request path %q does not start with escaped project path", r.URL.EscapedPath())
		}
		handler(w, r)
	}))
}

func writeJSON(t *testing.T, w http.ResponseWriter, v interface{}) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Fatalf("encoding response: %v", err)
	}
}

func TestGitLabIssueSourceDiscover(t *testing.T) {
	var issuesQuery url.Values
	server := gitlabTestServer(t, "glpat-test", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/project/issues"):
			issuesQuery = r.URL.Query()
			writeJSON(t, w, []map[string]interface{}{
				{
					"iid":         7,
					"title":       "Fix the bug",
					"description": "It crashes",
					"web_url":     "https://gitlab.com/group/subgroup/project/-/issues/7",
					"labels":      []string{"bug", "backend"},
					"author":      map[string]string{"username": "alice"},
				},
				{
					"iid":    8,
					"title":  "Excluded by label",
					"labels": []string{"wontfix"},
					"author": map[string]string{"username": "alice"},
				},
				{
					"iid":    9,
					"title":  "Excluded by author",
					"labels": []string{"bug"},
					"author": map[string]string{"username": "bot-user"},
				},
			})
		case strings.HasSuffix(r.URL.Path, "/issues/7/notes"):
			if got := r.URL.Query().Get("sort"); got != "asc" {
				t.Errorf("notes sort = %q, want asc", got)
			}
			writeJSON(t, w, []map[string]interface{}{
				{"body": "added label bug", "system": true, "author": map[string]string{"username": "alice"}},
				{"body": "first comment", "system": false, "author": map[string]string{"username": "bob"}},
				{"body": "second comment", "system": false, "author": map[string]string{"username": "alice"}},
			})
		default:
			t.Errorf("unexpected request path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	defer server.Close()

	src := &GitLabIssueSource{
		Project:        gitlabTestProject,
		Labels:         []string{"bug"},
		ExcludeLabels:  []string{"wontfix"},
		Author:         "alice",
		ExcludeAuthors: []string{"bot-user"},
		Token:          "glpat-test",
		BaseURL:        server.URL,
	}

	items, err := src.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("Discover() returned %d items, want 1: %+v", len(items), items)
	}

	item := items[0]
	if item.ID != "7" || item.Number != 7 {
		t.Errorf("item ID/Number = %q/%d, want 7/7", item.ID, item.Number)
	}
	if item.Title != "Fix the bug" || item.Body != "It crashes" {
		t.Errorf("item Title/Body = %q/%q", item.Title, item.Body)
	}
	if item.Kind != "Issue" {
		t.Errorf("item Kind = %q, want Issue", item.Kind)
	}
	if item.Comments != "first comment\n---\nsecond comment" {
		t.Errorf("item Comments = %q; system notes must be dropped", item.Comments)
	}

	if got := issuesQuery.Get("state"); got != "opened" {
		t.Errorf("state param = %q, want opened (mapped from default open)", got)
	}
	if got := issuesQuery.Get("labels"); got != "bug" {
		t.Errorf("labels param = %q, want bug", got)
	}
	if got := issuesQuery.Get("author_username"); got != "alice" {
		t.Errorf("author_username param = %q, want alice", got)
	}
}

func TestGitLabIssueSourcePagination(t *testing.T) {
	var pageRequests []string
	var server *httptest.Server
	server = gitlabTestServer(t, "", func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/notes") {
			writeJSON(t, w, []map[string]interface{}{})
			return
		}
		pageRequests = append(pageRequests, r.URL.RawQuery)
		if r.URL.Query().Get("page") == "" {
			w.Header().Set("Link", fmt.Sprintf(`<%s/projects/%s/issues?page=2&per_page=100>; rel="next"`, server.URL, url.PathEscape(gitlabTestProject)))
			writeJSON(t, w, []map[string]interface{}{
				{"iid": 1, "title": "first", "author": map[string]string{"username": "alice"}},
			})
			return
		}
		writeJSON(t, w, []map[string]interface{}{
			{"iid": 2, "title": "second", "author": map[string]string{"username": "alice"}},
		})
	})
	defer server.Close()

	src := &GitLabIssueSource{Project: gitlabTestProject, BaseURL: server.URL}
	items, err := src.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("Discover() returned %d items, want 2 across pages", len(items))
	}
	if len(pageRequests) != 2 {
		t.Fatalf("expected 2 issue page requests, got %d: %v", len(pageRequests), pageRequests)
	}
}

func TestGitLabIssueSourceStateAll(t *testing.T) {
	server := gitlabTestServer(t, "", func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/notes") {
			writeJSON(t, w, []map[string]interface{}{})
			return
		}
		if r.URL.Query().Has("state") {
			t.Errorf("state param should be omitted for all, got %q", r.URL.Query().Get("state"))
		}
		writeJSON(t, w, []map[string]interface{}{
			{"iid": 1, "title": "any", "author": map[string]string{"username": "alice"}},
		})
	})
	defer server.Close()

	src := &GitLabIssueSource{Project: gitlabTestProject, State: "all", BaseURL: server.URL}
	if _, err := src.Discover(context.Background()); err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
}

func TestGitLabIssueSourceAPIError(t *testing.T) {
	server := gitlabTestServer(t, "", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"message":"401 Unauthorized"}`)
	})
	defer server.Close()

	src := &GitLabIssueSource{Project: gitlabTestProject, BaseURL: server.URL}
	_, err := src.Discover(context.Background())
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("Discover() error = %v, want status 401 error", err)
	}
}

func TestGitLabMergeRequestSourceDiscover(t *testing.T) {
	var mrQuery url.Values
	server := gitlabTestServer(t, "glpat-test", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/project/merge_requests"):
			mrQuery = r.URL.Query()
			writeJSON(t, w, []map[string]interface{}{
				{
					"iid":           41,
					"title":         "Add feature",
					"description":   "Implements the thing",
					"web_url":       "https://gitlab.com/group/subgroup/project/-/merge_requests/41",
					"labels":        []string{"feature"},
					"author":        map[string]string{"username": "alice"},
					"source_branch": "feature-branch",
					"sha":           "abc123",
					"draft":         false,
				},
				{
					"iid":           42,
					"title":         "Draft: WIP",
					"labels":        []string{"feature"},
					"author":        map[string]string{"username": "alice"},
					"source_branch": "wip-branch",
					"sha":           "def456",
					"draft":         true,
				},
			})
		case strings.HasSuffix(r.URL.Path, "/merge_requests/41/notes"):
			writeJSON(t, w, []map[string]interface{}{
				{"body": "looks good", "system": false, "author": map[string]string{"username": "bob"}},
			})
		default:
			t.Errorf("unexpected request path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	defer server.Close()

	draft := false
	src := &GitLabMergeRequestSource{
		Project: gitlabTestProject,
		State:   "merged",
		Draft:   &draft,
		Token:   "glpat-test",
		BaseURL: server.URL,
	}

	items, err := src.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("Discover() returned %d items, want 1 (draft filtered): %+v", len(items), items)
	}

	item := items[0]
	if item.Kind != "MR" {
		t.Errorf("item Kind = %q, want MR", item.Kind)
	}
	if item.Branch != "feature-branch" {
		t.Errorf("item Branch = %q, want feature-branch", item.Branch)
	}
	if item.HeadSHA != "abc123" {
		t.Errorf("item HeadSHA = %q, want abc123", item.HeadSHA)
	}
	if item.Comments != "looks good" {
		t.Errorf("item Comments = %q", item.Comments)
	}
	if got := mrQuery.Get("state"); got != "merged" {
		t.Errorf("state param = %q, want merged", got)
	}
}
