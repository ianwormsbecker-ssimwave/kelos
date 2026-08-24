// Package gitprovider identifies the git hosting provider of a repository URL.
package gitprovider

import (
	"net/url"
	"strings"
)

// gitLabHost is the host of the GitLab.com SaaS service.
const gitLabHost = "gitlab.com"

// RepoHost extracts the host from a git repository URL. It supports
// https://host/path, git://host/path, ssh://git@host/path, and
// git@host:path forms. It returns an empty string when no host can be
// determined.
func RepoHost(repoURL string) string {
	repoURL = strings.TrimSpace(repoURL)
	if strings.Contains(repoURL, "://") {
		parsed, err := url.Parse(repoURL)
		if err != nil || parsed.Host == "" {
			return ""
		}
		return parsed.Hostname()
	}
	// SCP-like syntax: [user@]host:path
	if at := strings.Index(repoURL, "@"); at >= 0 {
		rest := repoURL[at+1:]
		if colon := strings.Index(rest, ":"); colon > 0 {
			return rest[:colon]
		}
	}
	return ""
}

// IsGitLab reports whether the repository URL is hosted on GitLab.com.
func IsGitLab(repoURL string) bool {
	return RepoHost(repoURL) == gitLabHost
}
