// pull-requests: the pull and merge requests of every repository you work in, on one page, after t3code's Pull
// requests view: grouped into those you wrote, those waiting on your review and the rest. The repositories are the
// ones the backend's terminals are in, and any you add; GitHub's are read with gh, GitLab's with glab.
package main

import (
	"cmp"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	paddock "github.com/getshepherd/extensions"
)

//go:embed web
var web embed.FS

// Repo is a repository on its host. Path is GitHub's owner/name or GitLab's full path, groups included.
type Repo struct {
	Host     string `json:"host"`
	Path     string `json:"path"`
	Provider string `json:"provider"` // github or gitlab
	Dir      string `json:"dir,omitempty"`
	Added    bool   `json:"added"` // by hand, rather than found in a terminal
}

func (r Repo) key() string { return r.Host + "/" + r.Path }

type Label struct {
	Name  string `json:"name"`
	Color string `json:"color,omitempty"`
}

// PR is one row of the list, a GitHub pull request or a GitLab merge request alike.
type PR struct {
	Provider   string    `json:"provider"`
	Host       string    `json:"host"`
	Repo       string    `json:"repo"`
	Number     int       `json:"number"`
	Title      string    `json:"title"`
	URL        string    `json:"url"`
	State      string    `json:"state"` // open, merged or closed
	Draft      bool      `json:"draft"`
	Author     string    `json:"author"`
	AuthorName string    `json:"authorName,omitempty"`
	Branch     string    `json:"branch"`
	Labels     []Label   `json:"labels"`
	Additions  int       `json:"additions"`
	Deletions  int       `json:"deletions"`
	Created    time.Time `json:"created"`
	Updated    time.Time `json:"updated"`
	Approved   bool      `json:"approved"`
	Avatar     string    `json:"avatar,omitempty"`
	Review     string    `json:"review"`    // approved, changes-requested, review-required or none
	Checks     string    `json:"checks"`    // passing, failing, pending, or empty with none
	Conflicts  bool      `json:"conflicts"` // with its base, as far as the host knows
	Mine       bool      `json:"mine"`      // you wrote it
	Reviewing  bool      `json:"reviewing"` // your review is asked for
}

type Listing struct {
	State  string    `json:"state"`
	Limit  int       `json:"limit"`
	More   bool      `json:"more"` // a repository had as many as the limit: there may be more
	PRs    []PR      `json:"prs"`
	Repos  []Repo    `json:"repos"`
	Errors []Failure `json:"errors"`
	At     time.Time `json:"at"`
}

type Failure struct {
	Repo  string `json:"repo"`
	Error string `json:"error"`
}

// fresh is how long a listing goes without being asked again. An older one is still answered at once, and asked
// for anew behind it: gh and glab take seconds for a dozen repositories, and the page shouldn't wait on them.
const fresh = time.Minute

var states = []string{"open", "merged", "closed", "all"}

// page is how many a repository's first listing reads, and Load more each time after. A hundred at once with their
// checks made GitHub's GraphQL answer 502 for a busy repository.
const page = 30

func main() {
	app := paddock.New("pull-requests", "Pull and merge requests of every repository you work in, on one page", web)
	// its page is about everything, not one workspace: the sidebar's, in place of the panes
	app.Show(paddock.Sidebar)
	added := filepath.Join(app.Dir(), "repos.json")
	c := newCache(app.Dir())

	paddock.Command(app, "list", "Pull requests of every repository, newest change first", func(_ *paddock.Ctx, in struct {
		State   string `json:"state" help:"open (default), merged, closed or all"`
		Limit   int    `json:"limit" help:"how many a repository, newest change first (30 by default)"`
		Refresh bool   `json:"refresh" help:"ask the hosts again instead of the last minute's answer"`
	}) (Listing, error) {
		state := cmp.Or(in.State, "open")
		if !slices.Contains(states, state) {
			return Listing{}, fmt.Errorf("state is one of %v", states)
		}
		limit := min(max(cmp.Or(in.Limit, page), 1), 500)
		repos, err := repos(added)
		if err != nil {
			return Listing{}, err
		}
		l, missing, stale := c.listing(repos, state, limit)
		if in.Refresh {
			missing, stale = repos, nil
		}
		// only the repositories never seen are waited for; the rest show their last answer, asked again behind it
		if len(missing) > 0 {
			c.ask(missing, state, limit)
			l, _, _ = c.listing(repos, state, limit)
		}
		if len(stale) > 0 {
			go func() {
				c.ask(stale, state, limit)
				l, _, _ := c.listing(repos, state, limit)
				app.Emit("listed", l) // open pages take it in place of the one they show
			}()
		}
		return l, nil
	})
	paddock.Command(app, "repos", "The repositories the list reads: the backend's terminals' and those added", func(_ *paddock.Ctx, _ struct{}) ([]Repo, error) {
		return repos(added)
	})
	paddock.Command(app, "session", "Which herdr session the repositories are read from; true when it is another than before", func(_ *paddock.Ctx, in struct {
		Socket string `json:"socket" help:"its API socket, as Shepherd's page event gives it; none for the default session"`
	}) (bool, error) {
		told.Store(true)
		return useSession(in.Socket), nil
	})
	paddock.Command(app, "add-repo", "Add a repository by its folder, its URL or host/owner/name", func(ctx *paddock.Ctx, in struct {
		Repo string `json:"repo" help:"a folder, a remote URL, or host/owner/name (github.com if no host)"`
	}) (Repo, error) {
		r, err := resolve(in.Repo)
		if err != nil {
			return Repo{}, err
		}
		r.Added = true
		list, err := load(added)
		if err != nil {
			return Repo{}, err
		}
		if !slices.ContainsFunc(list, func(o Repo) bool { return o.key() == r.key() }) {
			if err := save(added, append(list, r)); err != nil {
				return Repo{}, err
			}
		}
		ctx.Emit("repos", r)
		return r, nil
	})
	paddock.Command(app, "remove-repo", "Remove a repository added by hand", func(ctx *paddock.Ctx, in struct {
		Repo string `json:"repo" help:"host/path, as repos lists it"`
	}) (string, error) {
		list, err := load(added)
		if err != nil {
			return "", err
		}
		n := len(list)
		list = slices.DeleteFunc(list, func(r Repo) bool { return r.key() == in.Repo })
		if len(list) == n {
			return "", fmt.Errorf("%s wasn't added by hand", in.Repo)
		}
		if err := save(added, list); err != nil {
			return "", err
		}
		ctx.Emit("repos", in.Repo)
		return in.Repo, nil
	})

	for _, a := range []struct{ name, help string }{
		{"close", "Close a pull or merge request"},
		{"merge", "Merge a pull or merge request now"},
		{"ready", "Mark a draft ready for review"},
	} {
		paddock.Command(app, a.name, a.help, func(ctx *paddock.Ctx, in Target) (Target, error) {
			t, err := act(in, a.name)
			if err == nil {
				ctx.Emit("acted", t)
			}
			return t, err
		})
	}

	app.Run()
}

// remotes are the repositories of the main checkouts seen, by folder: a checkout's remote is read once.
// ponytail: a remote changed while the service runs is seen after its restart.
var remotes sync.Map

func remoteOf(dir string) (Repo, error) {
	if r, ok := remotes.Load(dir); ok {
		return r.(Repo), nil
	}
	r, err := fromDir(dir)
	if err == nil {
		remotes.Store(dir, r)
	}
	return r, err
}

// repos are the added ones and the backend's terminals', each once, sorted by path.
func repos(added string) ([]Repo, error) {
	list, err := load(added)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, r := range list {
		seen[r.key()] = true
	}
	// ponytail: a backend that can't be asked (not running) leaves only the added ones, silently
	dirs, _ := projects()
	for _, d := range dirs {
		if r, err := remoteOf(d); err == nil && !seen[r.key()] {
			seen[r.key()] = true
			list = append(list, r)
		}
	}
	slices.SortFunc(list, func(a, b Repo) int { return cmp.Compare(a.Path, b.Path) })
	return list, nil
}

// cache keeps each repository's last answer for each state and limit, in memory and on disk, so a page shows the
// repositories it has seen at once, those of another herdr session's too, and asks the hosts behind it.
type cache struct {
	dir    string
	mu     sync.Mutex
	at     map[string]answer // by entry: repository, state and limit
	asking map[string]bool   // entries being asked for, so one ask runs at a time
}

// answer is one repository's pull requests for a state and limit, or why they couldn't be read.
type answer struct {
	PRs   []PR      `json:"prs"`
	Error string    `json:"error,omitempty"`
	At    time.Time `json:"at"`
}

func entry(r Repo, state string, limit int) string {
	return fmt.Sprintf("%s %s-%d", r.key(), state, limit)
}

func newCache(dir string) *cache {
	c := &cache{dir: dir, at: map[string]answer{}, asking: map[string]bool{}}
	if b, err := os.ReadFile(c.file()); err == nil {
		_ = json.Unmarshal(b, &c.at)
	}
	// the listings an older version kept whole, one a state and limit
	old, _ := filepath.Glob(filepath.Join(dir, "listing-*.json"))
	for _, f := range old {
		os.Remove(f)
	}
	return c
}

func (c *cache) file() string { return filepath.Join(c.dir, "answers.json") }

// listing puts the answers kept for repos together, and says which repositories have none and which are older than
// fresh.
func (c *cache) listing(repos []Repo, state string, limit int) (l Listing, missing, stale []Repo) {
	l = Listing{State: state, Limit: limit, PRs: []PR{}, Repos: repos, Errors: []Failure{}}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, r := range repos {
		a, ok := c.at[entry(r, state, limit)]
		switch {
		case !ok:
			missing = append(missing, r)
			continue
		case time.Since(a.At) >= fresh:
			stale = append(stale, r)
		}
		if a.Error != "" {
			l.Errors = append(l.Errors, Failure{Repo: r.key(), Error: a.Error})
		}
		l.More = l.More || len(a.PRs) >= limit
		l.PRs = append(l.PRs, a.PRs...)
		// the listing is as old as its oldest answer
		if l.At.IsZero() || a.At.Before(l.At) {
			l.At = a.At
		}
	}
	slices.SortFunc(l.PRs, func(a, b PR) int { return b.Updated.Compare(a.Updated) })
	return l, missing, stale
}

// ask asks every host of repos at once and keeps the answers. A repository already being asked for is waited for
// rather than asked twice.
func (c *cache) ask(repos []Repo, state string, limit int) {
	var wg sync.WaitGroup
	for _, r := range repos {
		wg.Add(1)
		go func() {
			defer wg.Done()
			k := entry(r, state, limit)
			c.mu.Lock()
			if c.asking[k] {
				for c.asking[k] {
					c.mu.Unlock()
					time.Sleep(100 * time.Millisecond) // ponytail: polls; a sync.Cond if this ever matters
					c.mu.Lock()
				}
				c.mu.Unlock()
				return
			}
			c.asking[k] = true
			c.mu.Unlock()
			var prs []PR
			var err error
			if r.Provider == "github" {
				prs, err = github(r, state, limit)
			} else {
				prs, err = gitlab(r, state, limit)
			}
			a := answer{PRs: prs, At: time.Now()}
			if err != nil {
				a.Error = err.Error()
			}
			c.mu.Lock()
			c.at[k] = a
			delete(c.asking, k)
			c.mu.Unlock()
		}()
	}
	wg.Wait()
	c.mu.Lock()
	b, err := json.Marshal(c.at)
	c.mu.Unlock()
	if err == nil {
		_ = os.WriteFile(c.file(), b, 0o600)
	}
}

func load(path string) ([]Repo, error) {
	list := []Repo{}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return list, nil
	}
	if err != nil {
		return nil, err
	}
	return list, json.Unmarshal(b, &list)
}

func save(path string, list []Repo) error {
	b, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}
