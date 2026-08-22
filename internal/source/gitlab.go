package source

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// defaultGitLabBaseURL is the API base URL for GitLab.com.
const defaultGitLabBaseURL = "https://gitlab.com/api/v4"

// GitLabIssueSource discovers issues from a GitLab project.
type GitLabIssueSource struct {
	// Project is the full project path, e.g. "group/subgroup/project".
	Project        string
	Labels         []string
	ExcludeLabels  []string
	State          string
	Author         string
	ExcludeAuthors []string
	Token          string
	BaseURL        string
	Client         *http.Client
}

// GitLabMergeRequestSource discovers merge requests from a GitLab project.
type GitLabMergeRequestSource struct {
	// Project is the full project path, e.g. "group/subgroup/project".
	Project        string
	Labels         []string
	ExcludeLabels  []string
	State          string
	Author         string
	ExcludeAuthors []string
	Draft          *bool
	Token          string
	BaseURL        string
	Client         *http.Client
}

type gitlabUser struct {
	Username string `json:"username"`
}

type gitlabIssue struct {
	IID         int        `json:"iid"`
	Title       string     `json:"title"`
	Description string     `json:"description"`
	WebURL      string     `json:"web_url"`
	Labels      []string   `json:"labels"`
	Author      gitlabUser `json:"author"`
}

type gitlabMergeRequest struct {
	IID          int        `json:"iid"`
	Title        string     `json:"title"`
	Description  string     `json:"description"`
	WebURL       string     `json:"web_url"`
	Labels       []string   `json:"labels"`
	Author       gitlabUser `json:"author"`
	SourceBranch string     `json:"source_branch"`
	SHA          string     `json:"sha"`
	Draft        bool       `json:"draft"`
}

type gitlabNote struct {
	Body   string     `json:"body"`
	System bool       `json:"system"`
	Author gitlabUser `json:"author"`
}

// gitlabAPI holds the shared request plumbing for GitLab sources.
type gitlabAPI struct {
	baseURL string
	project string
	token   string
	client  *http.Client
}

func (a gitlabAPI) resolvedBaseURL() string {
	if a.baseURL != "" {
		return strings.TrimSuffix(a.baseURL, "/")
	}
	return defaultGitLabBaseURL
}

func (a gitlabAPI) httpClient() *http.Client {
	if a.client != nil {
		return a.client
	}
	return http.DefaultClient
}

// projectURL returns the API URL for a path under this project, with the
// project path URL-encoded as a single segment per the GitLab API convention.
func (a gitlabAPI) projectURL(subPath string) string {
	return a.resolvedBaseURL() + "/projects/" + url.PathEscape(a.project) + subPath
}

// getJSON fetches pageURL, decodes the JSON response into out, and returns
// the URL of the next page from the Link header (empty when this is the last
// page).
func (a gitlabAPI) getJSON(ctx context.Context, pageURL string, out interface{}) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
	if err != nil {
		return "", fmt.Errorf("creating request: %w", err)
	}
	if a.token != "" {
		req.Header.Set("Authorization", "Bearer "+a.token)
	}

	resp, err := a.httpClient().Do(req)
	if err != nil {
		return "", fmt.Errorf("fetching %s: %w", pageURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return "", fmt.Errorf("GitLab API returned status %d: %s", resp.StatusCode, string(body))
	}

	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return "", fmt.Errorf("decoding response: %w", err)
	}

	return parseNextLink(resp.Header.Get("Link")), nil
}

// gitlabStateParam maps the Kelos-facing state value to the GitLab API state
// parameter. An empty return value means the parameter is omitted (all states).
func gitlabStateParam(state string) string {
	switch state {
	case "", "open":
		return "opened"
	case "all":
		return ""
	default:
		return state
	}
}

// fetchNotes fetches all non-system notes for the given issue or merge
// request, oldest first, and returns them concatenated.
func (a gitlabAPI) fetchNotes(ctx context.Context, itemKind string, iid int) (string, error) {
	pageURL := a.projectURL(fmt.Sprintf("/%s/%d/notes?per_page=100&order_by=created_at&sort=asc", itemKind, iid))

	var bodies []string
	for page := 0; pageURL != "" && page < maxPages; page++ {
		var notes []gitlabNote
		nextURL, err := a.getJSON(ctx, pageURL, &notes)
		if err != nil {
			return "", err
		}
		for _, n := range notes {
			// System notes are activity records (label changes, assignments),
			// not conversation.
			if n.System {
				continue
			}
			bodies = append(bodies, n.Body)
		}
		pageURL = nextURL
	}

	return concatBodies(bodies), nil
}

// Discover fetches issues from GitLab and returns them as WorkItems.
func (s *GitLabIssueSource) Discover(ctx context.Context) ([]WorkItem, error) {
	api := gitlabAPI{baseURL: s.BaseURL, project: s.Project, token: s.Token, client: s.Client}

	params := url.Values{}
	params.Set("per_page", "100")
	if state := gitlabStateParam(s.State); state != "" {
		params.Set("state", state)
	}
	if len(s.Labels) > 0 {
		params.Set("labels", strings.Join(s.Labels, ","))
	}
	if s.Author != "" {
		params.Set("author_username", s.Author)
	}

	var issues []gitlabIssue
	pageURL := api.projectURL("/issues?" + params.Encode())
	for page := 0; pageURL != "" && page < maxPages; page++ {
		var pageIssues []gitlabIssue
		nextURL, err := api.getJSON(ctx, pageURL, &pageIssues)
		if err != nil {
			return nil, fmt.Errorf("fetching issues for project %s: %w", s.Project, err)
		}
		issues = append(issues, pageIssues...)
		pageURL = nextURL
	}

	var items []WorkItem
	for _, issue := range issues {
		if hasAnyLabel(issue.Labels, s.ExcludeLabels) || containsString(s.ExcludeAuthors, issue.Author.Username) {
			continue
		}

		comments, err := api.fetchNotes(ctx, "issues", issue.IID)
		if err != nil {
			return nil, fmt.Errorf("fetching notes for issue !%d: %w", issue.IID, err)
		}

		items = append(items, WorkItem{
			ID:       strconv.Itoa(issue.IID),
			Number:   issue.IID,
			Title:    issue.Title,
			Body:     issue.Description,
			URL:      issue.WebURL,
			Labels:   issue.Labels,
			Comments: comments,
			Kind:     "Issue",
		})
	}

	return items, nil
}

// Discover fetches merge requests from GitLab and returns them as WorkItems.
func (s *GitLabMergeRequestSource) Discover(ctx context.Context) ([]WorkItem, error) {
	api := gitlabAPI{baseURL: s.BaseURL, project: s.Project, token: s.Token, client: s.Client}

	params := url.Values{}
	params.Set("per_page", "100")
	if state := gitlabStateParam(s.State); state != "" {
		params.Set("state", state)
	}
	if len(s.Labels) > 0 {
		params.Set("labels", strings.Join(s.Labels, ","))
	}
	if s.Author != "" {
		params.Set("author_username", s.Author)
	}

	var mergeRequests []gitlabMergeRequest
	pageURL := api.projectURL("/merge_requests?" + params.Encode())
	for page := 0; pageURL != "" && page < maxPages; page++ {
		var pageMRs []gitlabMergeRequest
		nextURL, err := api.getJSON(ctx, pageURL, &pageMRs)
		if err != nil {
			return nil, fmt.Errorf("fetching merge requests for project %s: %w", s.Project, err)
		}
		mergeRequests = append(mergeRequests, pageMRs...)
		pageURL = nextURL
	}

	var items []WorkItem
	for _, mr := range mergeRequests {
		if hasAnyLabel(mr.Labels, s.ExcludeLabels) || containsString(s.ExcludeAuthors, mr.Author.Username) {
			continue
		}
		if s.Draft != nil && mr.Draft != *s.Draft {
			continue
		}

		comments, err := api.fetchNotes(ctx, "merge_requests", mr.IID)
		if err != nil {
			return nil, fmt.Errorf("fetching notes for merge request !%d: %w", mr.IID, err)
		}

		items = append(items, WorkItem{
			ID:       strconv.Itoa(mr.IID),
			Number:   mr.IID,
			Title:    mr.Title,
			Body:     mr.Description,
			URL:      mr.WebURL,
			Labels:   mr.Labels,
			Comments: comments,
			Kind:     "MR",
			Branch:   mr.SourceBranch,
			HeadSHA:  mr.SHA,
		})
	}

	return items, nil
}

func hasAnyLabel(labels, exclude []string) bool {
	for _, l := range labels {
		if containsString(exclude, l) {
			return true
		}
	}
	return false
}

func containsString(list []string, value string) bool {
	for _, v := range list {
		if v == value {
			return true
		}
	}
	return false
}
