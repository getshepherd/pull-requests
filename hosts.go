package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// A repository is its remote: upstream when there is one (a fork's pull requests live there), else origin.
func fromDir(dir string) (Repo, error) {
	out, err := run(dir, "git", "remote", "get-url", "upstream")
	if err != nil {
		out, err = run(dir, "git", "remote", "get-url", "origin")
	}
	if err != nil {
		return Repo{}, err
	}
	r, err := fromRemote(out)
	r.Dir = dir
	return r, err
}

// resolve reads what add-repo is given: a folder on disk, a remote URL, or host/owner/name.
func resolve(s string) (Repo, error) {
	s = strings.TrimSpace(s)
	if dir, err := expand(s); err == nil {
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			return fromDir(dir)
		}
	}
	if strings.Contains(s, "://") || strings.Contains(s, "@") {
		return fromRemote(s)
	}
	host, path, _ := strings.Cut(strings.Trim(s, "/"), "/")
	if !strings.Contains(host, ".") { // owner/name
		host, path = "github.com", strings.Trim(s, "/")
	}
	if strings.Count(path, "/") < 1 {
		return Repo{}, fmt.Errorf("%q is neither a folder, a URL nor host/owner/name", s)
	}
	return Repo{Host: host, Path: strings.TrimSuffix(path, ".git"), Provider: provider(host)}, nil
}

func expand(p string) (string, error) {
	if rest, ok := strings.CutPrefix(p, "~"); ok {
		home, err := os.UserHomeDir()
		return filepath.Join(home, rest), err
	}
	return filepath.Abs(p)
}

// fromRemote reads git's remote forms: https://host/path.git, ssh://git@host:22/path.git and git@host:path.git.
func fromRemote(u string) (Repo, error) {
	u = strings.TrimSuffix(strings.TrimSuffix(strings.TrimSpace(u), "/"), ".git")
	var host, path string
	if _, rest, ok := strings.Cut(u, "://"); ok {
		host, path, _ = strings.Cut(rest, "/")
		host = host[strings.LastIndex(host, "@")+1:]
		host, _, _ = strings.Cut(host, ":") // the port
	} else {
		host, path, _ = strings.Cut(u, ":")
		host = host[strings.LastIndex(host, "@")+1:]
	}
	path = strings.Trim(path, "/")
	if host == "" || !strings.Contains(path, "/") {
		return Repo{}, fmt.Errorf("can't read the remote %q", u)
	}
	return Repo{Host: host, Path: path, Provider: provider(host)}, nil
}

// ponytail: github.com is GitHub and every other host GitLab; a GitHub Enterprise host needs a provider field then.
func provider(host string) string {
	if host == "github.com" {
		return "github"
	}
	return "gitlab"
}

// viewers are each host's logged-in user, asked once.
var viewers sync.Map

func ghViewer(host string) string {
	if v, ok := viewers.Load(host); ok {
		return v.(string)
	}
	out, err := run("", "gh", "api", "--hostname", host, "user", "--jq", ".login")
	if err != nil {
		return ""
	}
	v := strings.TrimSpace(out)
	viewers.Store(host, v)
	return v
}

// github lists a repository's pull requests with gh: the newest hundred in the state.
func github(r Repo, state string, limit int) ([]PR, error) {
	usePATH()
	out, err := run("", "gh", "pr", "list", "-R", r.Host+"/"+r.Path, "--state", state, "--limit", strconv.Itoa(limit), "--json",
		"number,title,url,state,isDraft,author,headRefName,labels,additions,deletions,createdAt,updatedAt,reviewDecision,reviewRequests,mergeable,statusCheckRollup")
	if err != nil {
		return nil, err
	}
	var rows []struct {
		Number  int    `json:"number"`
		Title   string `json:"title"`
		URL     string `json:"url"`
		State   string `json:"state"`
		Draft   bool   `json:"isDraft"`
		Author  struct{ Login, Name string }
		Branch  string `json:"headRefName"`
		Labels  []struct{ Name, Color string }
		Add     int       `json:"additions"`
		Del     int       `json:"deletions"`
		Created time.Time `json:"createdAt"`
		Updated time.Time `json:"updatedAt"`
		Review  string    `json:"reviewDecision"`
		Asked   []struct {
			Login string `json:"login"`
		} `json:"reviewRequests"`
		Mergeable string  `json:"mergeable"`
		Rollup    []check `json:"statusCheckRollup"`
	}
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		return nil, err
	}
	me := ghViewer(r.Host)
	prs := make([]PR, 0, len(rows))
	for _, x := range rows {
		p := PR{Provider: "github", Host: r.Host, Repo: r.Path, Number: x.Number, Title: x.Title, URL: x.URL,
			State: strings.ToLower(x.State), Draft: x.Draft, Author: x.Author.Login, AuthorName: x.Author.Name,
			Branch: x.Branch, Labels: []Label{}, Additions: x.Add, Deletions: x.Del, Created: x.Created,
			Updated: x.Updated, Approved: x.Review == "APPROVED", Mine: me != "" && x.Author.Login == me,
			Avatar: "https://github.com/" + x.Author.Login + ".png?size=40", Conflicts: x.Mergeable == "CONFLICTING",
			Review: map[string]string{"APPROVED": "approved", "CHANGES_REQUESTED": "changes-requested", "REVIEW_REQUIRED": "review-required"}[x.Review]}
		if p.Review == "" {
			p.Review = "none"
		}
		p.Checks = rollup(x.Rollup)
		for _, l := range x.Labels {
			p.Labels = append(p.Labels, Label{Name: l.Name, Color: "#" + l.Color})
		}
		// ponytail: a request to a team you're on doesn't count; GitHub's search (review-requested:@me) would
		for _, a := range x.Asked {
			p.Reviewing = p.Reviewing || (me != "" && a.Login == me)
		}
		prs = append(prs, p)
	}
	return prs, nil
}

// One GraphQL read a project, as t3code does for the line counts the REST listing lacks; the viewer comes with it.
const mergeRequests = `query($path: ID!, $state: MergeRequestState, $first: Int) {
  currentUser { username }
  project(fullPath: $path) {
    mergeRequests(state: $state, first: $first, sort: UPDATED_DESC) {
      nodes {
        iid title webUrl state draft createdAt updatedAt sourceBranch approved conflicts detailedMergeStatus
        author { username name avatarUrl }
        labels { nodes { title color } }
        reviewers { nodes { username mergeRequestInteraction { reviewState } } }
        headPipeline { status }
        diffStatsSummary { additions deletions }
      }
    }
  }
}`

// gitlab lists a project's merge requests with glab: the hundred last changed in the state.
func gitlab(r Repo, state string, limit int) ([]PR, error) {
	usePATH()
	if state == "open" {
		state = "opened"
	}
	out, err := run("", "glab", "api", "graphql", "--hostname", r.Host, "-f", "query="+mergeRequests,
		"-f", "path="+r.Path, "-f", "state="+state, "-F", "first="+strconv.Itoa(min(limit, 100)))
	if err != nil {
		return nil, err
	}
	var res struct {
		Data struct {
			CurrentUser *struct{ Username string }
			Project     *struct {
				MergeRequests struct {
					Nodes []struct {
						IID      string `json:"iid"`
						Title    string
						WebURL   string `json:"webUrl"`
						State    string
						Draft    bool
						Created  time.Time `json:"createdAt"`
						Updated  time.Time `json:"updatedAt"`
						Branch   string    `json:"sourceBranch"`
						Approved bool
						Conflict bool                     `json:"conflicts"`
						Detailed string                   `json:"detailedMergeStatus"`
						Pipeline *struct{ Status string } `json:"headPipeline"`
						Author   struct {
							Username, Name string
							AvatarURL      string `json:"avatarUrl"`
						}
						Labels struct {
							Nodes []struct{ Title, Color string }
						}
						Reviewers struct {
							Nodes []struct {
								Username    string
								Interaction struct{ ReviewState string } `json:"mergeRequestInteraction"`
							}
						}
						Stats *struct{ Additions, Deletions int } `json:"diffStatsSummary"`
					}
				}
			}
		}
		Errors []struct{ Message string }
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		return nil, err
	}
	if len(res.Errors) > 0 {
		return nil, fmt.Errorf("%s", res.Errors[0].Message)
	}
	if res.Data.Project == nil {
		return nil, fmt.Errorf("no project %s on %s, or no access to it", r.Path, r.Host)
	}
	me := ""
	if res.Data.CurrentUser != nil {
		me = res.Data.CurrentUser.Username
	}
	prs := []PR{}
	for _, x := range res.Data.Project.MergeRequests.Nodes {
		n, _ := strconv.Atoi(x.IID)
		st := x.State
		if st == "opened" || st == "locked" {
			st = "open"
		}
		p := PR{Provider: "gitlab", Host: r.Host, Repo: r.Path, Number: n, Title: x.Title, URL: x.WebURL, State: st,
			Draft: x.Draft, Author: x.Author.Username, AuthorName: x.Author.Name, Branch: x.Branch, Labels: []Label{},
			Created: x.Created, Updated: x.Updated, Approved: x.Approved, Mine: me != "" && x.Author.Username == me,
			// `conflicts` is the last check, false again once the target moves on; the detailed status says CONFLICT
			Conflicts: x.Conflict || x.Detailed == "CONFLICT", Avatar: x.Author.AvatarURL, Review: "none"}
		if strings.HasPrefix(p.Avatar, "/") {
			p.Avatar = "https://" + r.Host + p.Avatar
		}
		if x.Pipeline != nil {
			p.Checks = map[string]string{"SUCCESS": "passing", "FAILED": "failing", "CANCELED": "failing",
				"RUNNING": "pending", "PENDING": "pending", "CREATED": "pending", "PREPARING": "pending",
				"WAITING_FOR_RESOURCE": "pending", "SCHEDULED": "pending"}[x.Pipeline.Status]
		}
		if len(x.Reviewers.Nodes) > 0 {
			p.Review = "review-required"
		}
		if x.Approved {
			p.Review = "approved"
		}
		if x.Stats != nil {
			p.Additions, p.Deletions = x.Stats.Additions, x.Stats.Deletions
		}
		for _, l := range x.Labels.Nodes {
			p.Labels = append(p.Labels, Label{Name: l.Title, Color: l.Color})
		}
		for _, v := range x.Reviewers.Nodes {
			p.Reviewing = p.Reviewing || (me != "" && v.Username == me)
			if v.Interaction.ReviewState == "REQUESTED_CHANGES" {
				p.Review = "changes-requested"
			}
		}
		prs = append(prs, p)
	}
	return prs, nil
}

// check is one of GitHub's status check rollup: a check run (its status and conclusion) or a status (its state).
type check struct{ Status, Conclusion, State string }

var failed = map[string]bool{"FAILURE": true, "TIMED_OUT": true, "CANCELLED": true, "ACTION_REQUIRED": true,
	"STARTUP_FAILURE": true, "ERROR": true}

// rollup is the checks' verdict: failing if any failed, else pending if any hasn't finished, else passing; empty
// without checks.
func rollup(checks []check) string {
	verdict := ""
	for _, c := range checks {
		switch {
		case failed[c.Conclusion] || failed[c.State]:
			return "failing"
		case (c.Status != "" && c.Status != "COMPLETED") || c.State == "PENDING" || c.State == "EXPECTED":
			verdict = "pending"
		case verdict == "":
			verdict = "passing"
		}
	}
	return verdict
}
