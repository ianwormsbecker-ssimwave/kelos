package webhook

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

// GitLabEventData represents parsed GitLab webhook data. Fields are populated
// by ParseGitLabWebhook from the payload's per-event shapes — they do not map
// 1:1 to the wire format so no JSON tags are used.
type GitLabEventData struct {
	ID     string
	Title  string
	Event  string // payload object_kind
	Action string
	Number int // issue or merge request IID
	Body   string
	URL    string
	State  string
	Labels []string
	// Branch is the push branch for push events and the source branch for
	// merge_request events (and note events on merge requests).
	Branch string
	Ref    string
	Tag    string
	Draft  bool
	Sender string
	// Project is the full project path, e.g. "group/subgroup/project".
	Project string
	// NoteableType is "Issue" or "MergeRequest" for note events.
	NoteableType string
	// CommentBody is the note text for note events.
	CommentBody string
	// HeadSHA is the merge request head commit for merge_request and note
	// events, and the after-commit for push events.
	HeadSHA string
	Payload map[string]interface{}
}

// gitlabWebhookPayload covers the payload fields Kelos reads across GitLab
// event types. GitLab reuses top-level keys between events; absent keys stay
// zero-valued.
type gitlabWebhookPayload struct {
	ObjectKind   string `json:"object_kind"`
	Ref          string `json:"ref"`
	After        string `json:"after"`
	UserUsername string `json:"user_username"` // push and tag_push events
	Action       string `json:"action"`        // release events
	Tag          string `json:"tag"`           // release events
	Name         string `json:"name"`          // release events
	Description  string `json:"description"`   // release events
	User         struct {
		Username string `json:"username"`
	} `json:"user"`
	Project struct {
		PathWithNamespace string `json:"path_with_namespace"`
		WebURL            string `json:"web_url"`
	} `json:"project"`
	ObjectAttributes struct {
		IID            int    `json:"iid"`
		Title          string `json:"title"`
		Description    string `json:"description"`
		State          string `json:"state"`
		Action         string `json:"action"`
		URL            string `json:"url"`
		SourceBranch   string `json:"source_branch"`
		Note           string `json:"note"`
		NoteableType   string `json:"noteable_type"`
		Draft          bool   `json:"draft"`
		WorkInProgress bool   `json:"work_in_progress"`
		Ref            string `json:"ref"`    // pipeline events
		Status         string `json:"status"` // pipeline events
		LastCommit     struct {
			ID string `json:"id"`
		} `json:"last_commit"`
	} `json:"object_attributes"`
	Labels []struct {
		Title string `json:"title"`
	} `json:"labels"`
	Issue *struct {
		IID         int    `json:"iid"`
		Title       string `json:"title"`
		Description string `json:"description"`
		State       string `json:"state"`
		Labels      []struct {
			Title string `json:"title"`
		} `json:"labels"`
	} `json:"issue"`
	MergeRequest *struct {
		IID          int    `json:"iid"`
		Title        string `json:"title"`
		Description  string `json:"description"`
		State        string `json:"state"`
		SourceBranch string `json:"source_branch"`
		LastCommit   struct {
			ID string `json:"id"`
		} `json:"last_commit"`
	} `json:"merge_request"`
}

// ParseGitLabWebhook parses a GitLab webhook payload. The event type is
// taken from the payload's object_kind field rather than the X-Gitlab-Event
// header, so matching does not depend on GitLab's human-readable header names.
func ParseGitLabWebhook(payload []byte) (*GitLabEventData, error) {
	var raw map[string]interface{}
	if err := json.Unmarshal(payload, &raw); err != nil {
		return nil, fmt.Errorf("invalid JSON payload: %w", err)
	}
	var p gitlabWebhookPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return nil, fmt.Errorf("invalid GitLab payload: %w", err)
	}

	eventData := &GitLabEventData{
		Event:   p.ObjectKind,
		Project: p.Project.PathWithNamespace,
		Sender:  p.User.Username,
		Payload: raw,
	}
	if eventData.Sender == "" {
		eventData.Sender = p.UserUsername
	}
	for _, l := range p.Labels {
		eventData.Labels = append(eventData.Labels, l.Title)
	}

	switch p.ObjectKind {
	case "push":
		eventData.Ref = p.Ref
		eventData.Branch = strings.TrimPrefix(p.Ref, "refs/heads/")
		eventData.HeadSHA = p.After
		eventData.ID = p.After
		eventData.Title = "Push to " + eventData.Branch

	case "tag_push":
		eventData.Ref = p.Ref
		eventData.Tag = strings.TrimPrefix(p.Ref, "refs/tags/")
		eventData.HeadSHA = p.After
		eventData.ID = p.After
		eventData.Title = "Tag " + eventData.Tag

	case "issue":
		eventData.Number = p.ObjectAttributes.IID
		eventData.ID = strconv.Itoa(p.ObjectAttributes.IID)
		eventData.Title = p.ObjectAttributes.Title
		eventData.Body = p.ObjectAttributes.Description
		eventData.State = p.ObjectAttributes.State
		eventData.Action = p.ObjectAttributes.Action
		eventData.URL = p.ObjectAttributes.URL

	case "merge_request":
		eventData.Number = p.ObjectAttributes.IID
		eventData.ID = strconv.Itoa(p.ObjectAttributes.IID)
		eventData.Title = p.ObjectAttributes.Title
		eventData.Body = p.ObjectAttributes.Description
		eventData.State = p.ObjectAttributes.State
		eventData.Action = p.ObjectAttributes.Action
		eventData.URL = p.ObjectAttributes.URL
		eventData.Branch = p.ObjectAttributes.SourceBranch
		eventData.Draft = p.ObjectAttributes.Draft || p.ObjectAttributes.WorkInProgress
		eventData.HeadSHA = p.ObjectAttributes.LastCommit.ID

	case "note":
		eventData.CommentBody = p.ObjectAttributes.Note
		eventData.NoteableType = p.ObjectAttributes.NoteableType
		eventData.URL = p.ObjectAttributes.URL
		switch {
		case p.Issue != nil:
			eventData.Number = p.Issue.IID
			eventData.ID = strconv.Itoa(p.Issue.IID)
			eventData.Title = p.Issue.Title
			eventData.Body = p.Issue.Description
			eventData.State = p.Issue.State
			for _, l := range p.Issue.Labels {
				eventData.Labels = append(eventData.Labels, l.Title)
			}
		case p.MergeRequest != nil:
			eventData.Number = p.MergeRequest.IID
			eventData.ID = strconv.Itoa(p.MergeRequest.IID)
			eventData.Title = p.MergeRequest.Title
			eventData.Body = p.MergeRequest.Description
			eventData.State = p.MergeRequest.State
			eventData.Branch = p.MergeRequest.SourceBranch
			eventData.HeadSHA = p.MergeRequest.LastCommit.ID
		}

	case "pipeline":
		eventData.Ref = p.ObjectAttributes.Ref
		eventData.Branch = p.ObjectAttributes.Ref
		eventData.State = p.ObjectAttributes.Status
		eventData.Title = "Pipeline " + p.ObjectAttributes.Status
		if p.MergeRequest != nil {
			eventData.Number = p.MergeRequest.IID
			eventData.ID = strconv.Itoa(p.MergeRequest.IID)
		}

	case "release":
		eventData.Action = p.Action
		eventData.Tag = p.Tag
		eventData.Title = p.Name
		eventData.Body = p.Description
		eventData.ID = p.Tag
	}

	return eventData, nil
}

// MatchesGitLabEvent checks whether a GitLab webhook event matches the given
// configuration.
func MatchesGitLabEvent(config *kelos.GitLabWebhook, eventData *GitLabEventData) (bool, error) {
	if config == nil || eventData == nil {
		return false, nil
	}

	if config.Project != "" && config.Project != eventData.Project {
		return false, nil
	}

	eventMatched := false
	for _, event := range config.Events {
		if strings.EqualFold(event, eventData.Event) {
			eventMatched = true
			break
		}
	}
	if !eventMatched {
		return false, nil
	}

	for _, excluded := range config.ExcludeAuthors {
		if strings.EqualFold(excluded, eventData.Sender) {
			return false, nil
		}
	}

	if len(config.Filters) == 0 {
		return true, nil
	}

	// OR semantics: any filter scoped to this event that matches accepts it.
	sawEventFilter := false
	for i := range config.Filters {
		filter := &config.Filters[i]
		if !strings.EqualFold(filter.Event, eventData.Event) {
			continue
		}
		sawEventFilter = true
		matched, err := matchesGitLabFilter(filter, eventData)
		if err != nil {
			return false, err
		}
		if matched {
			return true, nil
		}
	}

	// No filter is scoped to this event type — the Events list alone decides.
	return !sawEventFilter, nil
}

func matchesGitLabFilter(filter *kelos.GitLabWebhookFilter, eventData *GitLabEventData) (bool, error) {
	if filter.Action != "" && !strings.EqualFold(filter.Action, eventData.Action) {
		return false, nil
	}

	if filter.State != "" && !strings.EqualFold(filter.State, eventData.State) {
		return false, nil
	}

	if filter.NoteOn != "" && !strings.EqualFold(filter.NoteOn, eventData.NoteableType) {
		return false, nil
	}

	if filter.Author != "" && !strings.EqualFold(filter.Author, eventData.Sender) {
		return false, nil
	}
	for _, excluded := range filter.ExcludeAuthors {
		if strings.EqualFold(excluded, eventData.Sender) {
			return false, nil
		}
	}

	if filter.Draft != nil && eventData.Draft != *filter.Draft {
		return false, nil
	}

	if filter.Branch != "" {
		matched, err := filepath.Match(filter.Branch, eventData.Branch)
		if err != nil {
			return false, fmt.Errorf("invalid branch pattern %q: %w", filter.Branch, err)
		}
		if !matched {
			return false, nil
		}
	}

	if filter.Tag != "" {
		matched, err := filepath.Match(filter.Tag, eventData.Tag)
		if err != nil {
			return false, fmt.Errorf("invalid tag pattern %q: %w", filter.Tag, err)
		}
		if !matched {
			return false, nil
		}
	}

	if len(filter.Labels) > 0 || len(filter.ExcludeLabels) > 0 {
		present := make(map[string]bool, len(eventData.Labels))
		for _, l := range eventData.Labels {
			present[strings.ToLower(l)] = true
		}
		for _, required := range filter.Labels {
			if !present[strings.ToLower(required)] {
				return false, nil
			}
		}
		for _, excluded := range filter.ExcludeLabels {
			if present[strings.ToLower(excluded)] {
				return false, nil
			}
		}
	}

	if filter.BodyPattern != "" {
		// Note events match against the note text; other events match
		// against the item description.
		body := eventData.Body
		if eventData.Event == "note" {
			body = eventData.CommentBody
		}
		re, err := regexp.Compile(filter.BodyPattern)
		if err != nil {
			return false, fmt.Errorf("invalid body pattern %q: %w", filter.BodyPattern, err)
		}
		if !re.MatchString(body) {
			return false, nil
		}
	}

	return true, nil
}

// ExtractGitLabWorkItem converts GitLab webhook data to template variables.
func ExtractGitLabWorkItem(eventData *GitLabEventData) map[string]interface{} {
	return map[string]interface{}{
		"ID":          eventData.ID,
		"Title":       eventData.Title,
		"Kind":        "webhook",
		"Event":       eventData.Event,
		"Action":      eventData.Action,
		"Number":      eventData.Number,
		"Body":        eventData.Body,
		"URL":         eventData.URL,
		"State":       eventData.State,
		"Labels":      strings.Join(eventData.Labels, ", "),
		"Branch":      eventData.Branch,
		"Ref":         eventData.Ref,
		"Tag":         eventData.Tag,
		"Draft":       eventData.Draft,
		"Sender":      eventData.Sender,
		"Project":     eventData.Project,
		"CommentBody": eventData.CommentBody,
		"HeadSHA":     eventData.HeadSHA,
		"Payload":     eventData.Payload,
	}
}

// gitlabWebhookSourceKind determines the reporting source kind from a GitLab
// webhook event.
func gitlabWebhookSourceKind(eventData *GitLabEventData) string {
	switch eventData.Event {
	case "merge_request":
		return "merge-request"
	case "note":
		if eventData.NoteableType == "MergeRequest" {
			return "merge-request"
		}
		return "issue"
	case "pipeline":
		if eventData.Number > 0 {
			return "merge-request"
		}
		return "issue"
	default:
		return "issue"
	}
}
