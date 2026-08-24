package reporting

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

const defaultGitLabBaseURL = "https://gitlab.com/api/v4"

// GitLabReporter posts and updates issue/merge request notes on GitLab.
// TokenFunc, when set, is called on every API request to resolve the current
// token; otherwise the static Token field is used.
type GitLabReporter struct {
	// Project is the full project path, e.g. "group/subgroup/project".
	Project string
	// ItemKind is the notes API resource: "issues" or "merge_requests".
	ItemKind  string
	Token     string        // static token (used when TokenFunc is nil)
	TokenFunc func() string // dynamic token resolver; takes precedence over Token
	BaseURL   string
	Client    *http.Client
}

func (r *GitLabReporter) baseURL() string {
	if r.BaseURL != "" {
		return strings.TrimSuffix(r.BaseURL, "/")
	}
	return defaultGitLabBaseURL
}

func (r *GitLabReporter) httpClient() *http.Client {
	if r.Client != nil {
		return r.Client
	}
	return http.DefaultClient
}

func (r *GitLabReporter) notesURL(number int) string {
	return fmt.Sprintf("%s/projects/%s/%s/%d/notes", r.baseURL(), url.PathEscape(r.Project), r.ItemKind, number)
}

type gitlabNoteRequest struct {
	Body string `json:"body"`
}

type gitlabNoteResponse struct {
	ID     int64          `json:"id"`
	Body   string         `json:"body"`
	Author gitlabNoteUser `json:"author"`
}

type gitlabNoteUser struct {
	Username string `json:"username"`
}

// FindCommentByMarker returns the newest note containing marker that was
// authored by the token's user, or zero when no matching note exists.
func (r *GitLabReporter) FindCommentByMarker(ctx context.Context, number int, marker string) (int64, error) {
	author, err := r.tokenUsername(ctx)
	if err != nil {
		return 0, err
	}

	var foundID int64
	for page := 1; ; page++ {
		pageURL := fmt.Sprintf("%s?per_page=100&page=%d&order_by=created_at&sort=asc", r.notesURL(number), page)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
		if err != nil {
			return 0, fmt.Errorf("creating request: %w", err)
		}
		r.setHeaders(req)

		resp, err := r.httpClient().Do(req)
		if err != nil {
			return 0, fmt.Errorf("listing notes: %w", err)
		}
		if resp.StatusCode != http.StatusOK {
			errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
			resp.Body.Close()
			return 0, fmt.Errorf("GitLab API returned status %d: %s", resp.StatusCode, string(errBody))
		}

		var notes []gitlabNoteResponse
		if err := json.NewDecoder(resp.Body).Decode(&notes); err != nil {
			resp.Body.Close()
			return 0, fmt.Errorf("decoding notes response: %w", err)
		}
		resp.Body.Close()

		for _, note := range notes {
			if strings.Contains(note.Body, marker) && strings.EqualFold(note.Author.Username, author) {
				foundID = note.ID
			}
		}
		if len(notes) < 100 {
			return foundID, nil
		}
	}
}

// tokenUsername resolves the username the token authenticates as, so sticky
// note lookup only matches notes this reporter authored.
func (r *GitLabReporter) tokenUsername(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.baseURL()+"/user", nil)
	if err != nil {
		return "", fmt.Errorf("creating request: %w", err)
	}
	r.setHeaders(req)

	resp, err := r.httpClient().Do(req)
	if err != nil {
		return "", fmt.Errorf("getting authenticated GitLab user: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return "", fmt.Errorf("GitLab API returned status %d: %s", resp.StatusCode, string(errBody))
	}

	var user gitlabNoteUser
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return "", fmt.Errorf("decoding authenticated user response: %w", err)
	}
	if user.Username == "" {
		return "", fmt.Errorf("authenticated GitLab user response has no username")
	}
	return user.Username, nil
}

// CreateComment creates a note on a GitLab issue or merge request and
// returns the note ID.
func (r *GitLabReporter) CreateComment(ctx context.Context, number int, body string) (int64, error) {
	payload, err := json.Marshal(gitlabNoteRequest{Body: body})
	if err != nil {
		return 0, fmt.Errorf("marshalling note body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.notesURL(number), bytes.NewReader(payload))
	if err != nil {
		return 0, fmt.Errorf("creating request: %w", err)
	}
	r.setHeaders(req)

	resp, err := r.httpClient().Do(req)
	if err != nil {
		return 0, fmt.Errorf("posting note: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return 0, fmt.Errorf("GitLab API returned status %d: %s", resp.StatusCode, string(errBody))
	}

	var result gitlabNoteResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return 0, fmt.Errorf("decoding note response: %w", err)
	}

	return result.ID, nil
}

// UpdateComment updates an existing note on the given GitLab issue or merge
// request by its note ID.
func (r *GitLabReporter) UpdateComment(ctx context.Context, number int, commentID int64, body string) error {
	payload, err := json.Marshal(gitlabNoteRequest{Body: body})
	if err != nil {
		return fmt.Errorf("marshalling note body: %w", err)
	}

	noteURL := r.notesURL(number) + "/" + strconv.FormatInt(commentID, 10)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, noteURL, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	r.setHeaders(req)

	resp, err := r.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("updating note: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("GitLab API returned status %d: %s", resp.StatusCode, string(errBody))
	}

	return nil
}

func (r *GitLabReporter) setHeaders(req *http.Request) {
	token := r.Token
	if r.TokenFunc != nil {
		token = r.TokenFunc()
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")
}
