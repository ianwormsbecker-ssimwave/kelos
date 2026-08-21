package gitprovider

import "testing"

func TestRepoHost(t *testing.T) {
	tests := []struct {
		name    string
		repoURL string
		want    string
	}{
		{name: "https github", repoURL: "https://github.com/owner/repo.git", want: "github.com"},
		{name: "https gitlab", repoURL: "https://gitlab.com/group/project.git", want: "gitlab.com"},
		{name: "https gitlab nested subgroup", repoURL: "https://gitlab.com/group/subgroup/project.git", want: "gitlab.com"},
		{name: "https with username", repoURL: "https://oauth2@gitlab.com/group/project.git", want: "gitlab.com"},
		{name: "https with port", repoURL: "https://gitlab.example.com:8443/group/project.git", want: "gitlab.example.com"},
		{name: "scp-like ssh", repoURL: "git@gitlab.com:group/project.git", want: "gitlab.com"},
		{name: "ssh scheme", repoURL: "ssh://git@gitlab.com/group/project.git", want: "gitlab.com"},
		{name: "git scheme", repoURL: "git://github.com/owner/repo.git", want: "github.com"},
		{name: "no host", repoURL: "owner/repo", want: ""},
		{name: "empty", repoURL: "", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := RepoHost(tt.repoURL); got != tt.want {
				t.Errorf("RepoHost(%q) = %q, want %q", tt.repoURL, got, tt.want)
			}
		})
	}
}

func TestIsGitLab(t *testing.T) {
	tests := []struct {
		repoURL string
		want    bool
	}{
		{repoURL: "https://gitlab.com/group/project.git", want: true},
		{repoURL: "https://gitlab.com/group/subgroup/project.git", want: true},
		{repoURL: "git@gitlab.com:group/project.git", want: true},
		{repoURL: "https://github.com/owner/repo.git", want: false},
		{repoURL: "https://gitlab.example.com/group/project.git", want: false},
		{repoURL: "https://mygitlab.com/group/project.git", want: false},
	}
	for _, tt := range tests {
		if got := IsGitLab(tt.repoURL); got != tt.want {
			t.Errorf("IsGitLab(%q) = %v, want %v", tt.repoURL, got, tt.want)
		}
	}
}
