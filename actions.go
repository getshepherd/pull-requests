package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// A row's quick actions, after t3code's (held Shift shows them), run as Shepherd's review panel runs them, but by
// host and path: a row may have no checkout.

type Target struct {
	Repo   string `json:"repo" help:"host/path, a PR's host and repo as list prints them"`
	Number int    `json:"number" help:"the PR's or MR's number"`
}

func act(t Target, action string) (Target, error) {
	usePATH()
	host, path, ok := strings.Cut(t.Repo, "/")
	if !ok || !strings.Contains(path, "/") || t.Number <= 0 {
		return t, fmt.Errorf("a repo as host/owner/name and a number, not %q #%d", t.Repo, t.Number)
	}
	n := strconv.Itoa(t.Number)
	var err error
	if provider(host) == "github" {
		switch action {
		case "close", "ready":
			_, err = run("", "gh", "pr", action, n, "-R", t.Repo)
		case "merge":
			_, err = run("", "gh", "pr", "merge", n, mergeWay(t.Repo), "-R", t.Repo)
		}
		return t, err
	}
	mr := "projects/" + url.PathEscape(path) + "/merge_requests/" + n
	switch action {
	case "close":
		_, err = run("", "glab", "api", "--hostname", host, "-X", "PUT", mr, "-f", "state_event=close")
	case "merge":
		// nothing but the merge: the MR's own squash and branch removal settings win
		_, err = run("", "glab", "api", "--hostname", host, "-X", "PUT", mr+"/merge")
	case "ready":
		var out string
		out, err = run("", "glab", "api", "graphql", "--hostname", host,
			"-f", "query=mutation($p: ID!, $i: String!) { mergeRequestSetDraft(input: {projectPath: $p, iid: $i, draft: false}) { errors } }",
			"-f", "p="+path, "-f", "i="+n)
		var res struct {
			Data struct {
				Set struct{ Errors []string } `json:"mergeRequestSetDraft"`
			}
			Errors []struct{ Message string }
		}
		if err == nil && json.Unmarshal([]byte(out), &res) == nil {
			if len(res.Errors) > 0 {
				err = fmt.Errorf("%s", res.Errors[0].Message)
			} else if len(res.Data.Set.Errors) > 0 {
				err = fmt.Errorf("%s", res.Data.Set.Errors[0])
			}
		}
	}
	return t, err
}

// mergeWay is the first way the repository allows of a merge commit, a rebase and a squash, as the review panel
// picks it; a merge commit when gh can't say.
func mergeWay(repo string) string {
	out, err := run("", "gh", "repo", "view", repo, "--json", "mergeCommitAllowed,rebaseMergeAllowed,squashMergeAllowed")
	var allowed map[string]bool
	if err != nil || json.Unmarshal([]byte(out), &allowed) != nil {
		return "--merge"
	}
	for _, w := range [][2]string{{"mergeCommitAllowed", "--merge"}, {"rebaseMergeAllowed", "--rebase"}, {"squashMergeAllowed", "--squash"}} {
		if allowed[w[0]] {
			return w[1]
		}
	}
	return "--merge"
}
