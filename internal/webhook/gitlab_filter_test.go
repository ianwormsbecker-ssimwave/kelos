package webhook

import (
	"testing"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

const gitlabMergeRequestPayload = `{
	"object_kind": "merge_request",
	"user": {"username": "alice"},
	"project": {"path_with_namespace": "group/subgroup/project", "web_url": "https://gitlab.com/group/subgroup/project"},
	"object_attributes": {
		"iid": 41,
		"title": "Add feature",
		"description": "Implements the thing",
		"state": "opened",
		"action": "open",
		"url": "https://gitlab.com/group/subgroup/project/-/merge_requests/41",
		"source_branch": "feature-branch",
		"draft": false,
		"work_in_progress": false,
		"last_commit": {"id": "abc123"}
	},
	"labels": [{"title": "needs-review"}, {"title": "backend"}]
}`

const gitlabIssuePayload = `{
	"object_kind": "issue",
	"user": {"username": "bob"},
	"project": {"path_with_namespace": "group/project"},
	"object_attributes": {
		"iid": 7,
		"title": "Fix the bug",
		"description": "It crashes",
		"state": "opened",
		"action": "open",
		"url": "https://gitlab.com/group/project/-/issues/7"
	},
	"labels": [{"title": "bug"}]
}`

const gitlabNotePayload = `{
	"object_kind": "note",
	"user": {"username": "carol"},
	"project": {"path_with_namespace": "group/project"},
	"object_attributes": {
		"note": "/kelos take a look",
		"noteable_type": "MergeRequest",
		"url": "https://gitlab.com/group/project/-/merge_requests/41#note_99"
	},
	"merge_request": {
		"iid": 41,
		"title": "Add feature",
		"description": "Implements the thing",
		"state": "opened",
		"source_branch": "feature-branch",
		"last_commit": {"id": "abc123"}
	}
}`

const gitlabPushPayload = `{
	"object_kind": "push",
	"ref": "refs/heads/main",
	"after": "def456",
	"user_username": "dave",
	"project": {"path_with_namespace": "group/project"}
}`

func TestParseGitLabWebhook(t *testing.T) {
	mr, err := ParseGitLabWebhook([]byte(gitlabMergeRequestPayload))
	if err != nil {
		t.Fatalf("ParseGitLabWebhook(merge_request) error = %v", err)
	}
	if mr.Event != "merge_request" || mr.Number != 41 || mr.ID != "41" {
		t.Errorf("merge_request parse: Event=%q Number=%d ID=%q", mr.Event, mr.Number, mr.ID)
	}
	if mr.Branch != "feature-branch" || mr.HeadSHA != "abc123" || mr.Sender != "alice" {
		t.Errorf("merge_request parse: Branch=%q HeadSHA=%q Sender=%q", mr.Branch, mr.HeadSHA, mr.Sender)
	}
	if mr.Project != "group/subgroup/project" || mr.Action != "open" || mr.State != "opened" {
		t.Errorf("merge_request parse: Project=%q Action=%q State=%q", mr.Project, mr.Action, mr.State)
	}
	if len(mr.Labels) != 2 || mr.Labels[0] != "needs-review" {
		t.Errorf("merge_request parse: Labels=%v", mr.Labels)
	}

	note, err := ParseGitLabWebhook([]byte(gitlabNotePayload))
	if err != nil {
		t.Fatalf("ParseGitLabWebhook(note) error = %v", err)
	}
	if note.Event != "note" || note.Number != 41 || note.NoteableType != "MergeRequest" {
		t.Errorf("note parse: Event=%q Number=%d NoteableType=%q", note.Event, note.Number, note.NoteableType)
	}
	if note.CommentBody != "/kelos take a look" || note.Branch != "feature-branch" || note.HeadSHA != "abc123" {
		t.Errorf("note parse: CommentBody=%q Branch=%q HeadSHA=%q", note.CommentBody, note.Branch, note.HeadSHA)
	}

	push, err := ParseGitLabWebhook([]byte(gitlabPushPayload))
	if err != nil {
		t.Fatalf("ParseGitLabWebhook(push) error = %v", err)
	}
	if push.Event != "push" || push.Branch != "main" || push.Ref != "refs/heads/main" {
		t.Errorf("push parse: Event=%q Branch=%q Ref=%q", push.Event, push.Branch, push.Ref)
	}
	if push.Sender != "dave" || push.HeadSHA != "def456" {
		t.Errorf("push parse: Sender=%q HeadSHA=%q", push.Sender, push.HeadSHA)
	}
}

func TestMatchesGitLabEvent(t *testing.T) {
	mr, _ := ParseGitLabWebhook([]byte(gitlabMergeRequestPayload))
	issue, _ := ParseGitLabWebhook([]byte(gitlabIssuePayload))
	note, _ := ParseGitLabWebhook([]byte(gitlabNotePayload))
	push, _ := ParseGitLabWebhook([]byte(gitlabPushPayload))
	draft := true

	tests := []struct {
		name   string
		config *kelos.GitLabWebhook
		event  *GitLabEventData
		want   bool
	}{
		{
			name:   "event type match without filters",
			config: &kelos.GitLabWebhook{Events: []string{"merge_request"}},
			event:  mr,
			want:   true,
		},
		{
			name:   "event type mismatch",
			config: &kelos.GitLabWebhook{Events: []string{"issue"}},
			event:  mr,
			want:   false,
		},
		{
			name:   "project restriction match",
			config: &kelos.GitLabWebhook{Events: []string{"merge_request"}, Project: "group/subgroup/project"},
			event:  mr,
			want:   true,
		},
		{
			name:   "project restriction mismatch",
			config: &kelos.GitLabWebhook{Events: []string{"merge_request"}, Project: "other/project"},
			event:  mr,
			want:   false,
		},
		{
			name:   "top-level exclude author",
			config: &kelos.GitLabWebhook{Events: []string{"merge_request"}, ExcludeAuthors: []string{"alice"}},
			event:  mr,
			want:   false,
		},
		{
			name: "action and label filter match",
			config: &kelos.GitLabWebhook{Events: []string{"merge_request"}, Filters: []kelos.GitLabWebhookFilter{
				{Event: "merge_request", Action: "open", Labels: []string{"needs-review"}},
			}},
			event: mr,
			want:  true,
		},
		{
			name: "label filter requires all labels",
			config: &kelos.GitLabWebhook{Events: []string{"merge_request"}, Filters: []kelos.GitLabWebhookFilter{
				{Event: "merge_request", Labels: []string{"needs-review", "urgent"}},
			}},
			event: mr,
			want:  false,
		},
		{
			name: "exclude label filter",
			config: &kelos.GitLabWebhook{Events: []string{"merge_request"}, Filters: []kelos.GitLabWebhookFilter{
				{Event: "merge_request", ExcludeLabels: []string{"backend"}},
			}},
			event: mr,
			want:  false,
		},
		{
			name: "draft filter rejects non-draft mismatch",
			config: &kelos.GitLabWebhook{Events: []string{"merge_request"}, Filters: []kelos.GitLabWebhookFilter{
				{Event: "merge_request", Draft: &draft},
			}},
			event: mr,
			want:  false,
		},
		{
			name: "branch glob filter",
			config: &kelos.GitLabWebhook{Events: []string{"merge_request"}, Filters: []kelos.GitLabWebhookFilter{
				{Event: "merge_request", Branch: "feature-*"},
			}},
			event: mr,
			want:  true,
		},
		{
			name: "push branch filter",
			config: &kelos.GitLabWebhook{Events: []string{"push"}, Filters: []kelos.GitLabWebhookFilter{
				{Event: "push", Branch: "main"},
			}},
			event: push,
			want:  true,
		},
		{
			name: "note body pattern matches note text",
			config: &kelos.GitLabWebhook{Events: []string{"note"}, Filters: []kelos.GitLabWebhookFilter{
				{Event: "note", BodyPattern: `^/kelos\b`, NoteOn: kelos.NoteOnMergeRequest},
			}},
			event: note,
			want:  true,
		},
		{
			name: "note scoped to issues does not match MR note",
			config: &kelos.GitLabWebhook{Events: []string{"note"}, Filters: []kelos.GitLabWebhookFilter{
				{Event: "note", NoteOn: kelos.NoteOnIssue},
			}},
			event: note,
			want:  false,
		},
		{
			name: "filter for a different event leaves this event matched by Events alone",
			config: &kelos.GitLabWebhook{Events: []string{"issue", "merge_request"}, Filters: []kelos.GitLabWebhookFilter{
				{Event: "merge_request", Labels: []string{"needs-review"}},
			}},
			event: issue,
			want:  true,
		},
		{
			name: "OR semantics across filters for the same event",
			config: &kelos.GitLabWebhook{Events: []string{"merge_request"}, Filters: []kelos.GitLabWebhookFilter{
				{Event: "merge_request", Action: "close"},
				{Event: "merge_request", Action: "open"},
			}},
			event: mr,
			want:  true,
		},
		{
			name: "filter author match",
			config: &kelos.GitLabWebhook{Events: []string{"issue"}, Filters: []kelos.GitLabWebhookFilter{
				{Event: "issue", Author: "bob"},
			}},
			event: issue,
			want:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := MatchesGitLabEvent(tt.config, tt.event)
			if err != nil {
				t.Fatalf("MatchesGitLabEvent() error = %v", err)
			}
			if got != tt.want {
				t.Errorf("MatchesGitLabEvent() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMatchesGitLabEventInvalidBodyPattern(t *testing.T) {
	note, _ := ParseGitLabWebhook([]byte(gitlabNotePayload))
	config := &kelos.GitLabWebhook{Events: []string{"note"}, Filters: []kelos.GitLabWebhookFilter{
		{Event: "note", BodyPattern: "["},
	}}
	if _, err := MatchesGitLabEvent(config, note); err == nil {
		t.Fatal("MatchesGitLabEvent() expected error for invalid regex")
	}
}

func TestExtractGitLabWorkItem(t *testing.T) {
	note, _ := ParseGitLabWebhook([]byte(gitlabNotePayload))
	vars := ExtractGitLabWorkItem(note)

	if vars["Event"] != "note" || vars["Number"] != 41 || vars["Kind"] != "webhook" {
		t.Errorf("vars Event/Number/Kind = %v/%v/%v", vars["Event"], vars["Number"], vars["Kind"])
	}
	if vars["CommentBody"] != "/kelos take a look" {
		t.Errorf("vars CommentBody = %v", vars["CommentBody"])
	}
	if vars["Branch"] != "feature-branch" || vars["Project"] != "group/project" {
		t.Errorf("vars Branch/Project = %v/%v", vars["Branch"], vars["Project"])
	}
	if vars["Payload"] == nil {
		t.Error("vars Payload missing")
	}
}

func TestGitLabWebhookSourceKind(t *testing.T) {
	mr, _ := ParseGitLabWebhook([]byte(gitlabMergeRequestPayload))
	issue, _ := ParseGitLabWebhook([]byte(gitlabIssuePayload))
	note, _ := ParseGitLabWebhook([]byte(gitlabNotePayload))

	if got := gitlabWebhookSourceKind(mr); got != "merge-request" {
		t.Errorf("merge_request kind = %q", got)
	}
	if got := gitlabWebhookSourceKind(issue); got != "issue" {
		t.Errorf("issue kind = %q", got)
	}
	if got := gitlabWebhookSourceKind(note); got != "merge-request" {
		t.Errorf("MR note kind = %q", got)
	}
}
