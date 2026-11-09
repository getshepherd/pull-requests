package main

// The backend's terminals and the commands they're asked with: automations' launch.go (getshepherd/automations), the
// parts a list of repositories needs.

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	paddock "github.com/getshepherd/extensions"
)

// shepherdBackend is the backend Shepherd shows, from its settings beside the extensions folder.
func shepherdBackend() string {
	b, err := os.ReadFile(filepath.Join(filepath.Dir(paddock.ExtensionsDir()), "settings.json"))
	var s struct {
		Backend string `json:"backend"`
	}
	if err != nil || json.Unmarshal(b, &s) != nil || s.Backend == "" {
		return "herdr" // Shepherd's default
	}
	return s.Backend
}

var loginPath sync.Once

// usePATH gives the service the login shell's PATH: launchd and systemd start it with a bare one, where neither the
// agents nor the backends' CLIs are.
func usePATH() {
	loginPath.Do(func() {
		shell := os.Getenv("SHELL")
		if shell == "" {
			shell = "/bin/sh"
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, shell, "-l", "-c", `printf %s "$PATH"`).Output()
		if err == nil && len(out) > 0 {
			os.Setenv("PATH", string(out))
		}
		// Rex puts its CLI on the PATH of its own terminals only; Shepherd finds it in the app, as this does
		home, _ := os.UserHomeDir()
		rex := filepath.Join(home, "Library/Application Support/com.superlogical.rex/RexServer/Versions/Current/Rex Server.app/Contents/MacOS")
		if _, err := os.Stat(filepath.Join(rex, "rex")); err == nil {
			os.Setenv("PATH", os.Getenv("PATH")+string(os.PathListSeparator)+rex)
		}
	})
}

func run(dir string, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errOut.String())
		if msg == "" {
			msg = strings.TrimSpace(out.String())
		}
		return "", fmt.Errorf("%s %s: %v: %s", name, strings.Join(args[:min(len(args), 2)], " "), err, msg)
	}
	return out.String(), nil
}

// field reads one value out of a command's JSON with a dotted path, as jq's `.a.b[0].c`.
func field(out, path string) string {
	var v any
	if json.Unmarshal([]byte(out), &v) != nil {
		return ""
	}
	for _, key := range strings.Split(strings.TrimPrefix(path, "."), ".") {
		name, index, list := strings.Cut(strings.TrimSuffix(key, "]"), "[")
		if m, ok := v.(map[string]any); ok {
			v = m[name]
		} else {
			return ""
		}
		if list {
			a, ok := v.([]any)
			if !ok || len(a) == 0 || index != "0" {
				return ""
			}
			v = a[0]
		}
	}
	switch v := v.(type) {
	case string:
		return v
	case float64:
		return fmt.Sprint(v)
	}
	return ""
}

// projects are the repositories the backend Shepherd shows has terminals in, each once by its main checkout: a
// worktree counts as the repository it belongs to, and a folder outside git is left out.
func projects() ([]string, error) {
	usePATH()
	var dirs []string
	switch backend := shepherdBackend(); backend {
	case "herdr":
		out, err := run("", "herdr", "workspace", "list")
		if err != nil {
			return nil, err
		}
		var r struct {
			Result struct {
				Workspaces []struct {
					Worktree struct {
						RepoRoot string `json:"repo_root"`
					} `json:"worktree"`
				} `json:"workspaces"`
			} `json:"result"`
		}
		if err := json.Unmarshal([]byte(out), &r); err != nil {
			return nil, err
		}
		for _, w := range r.Result.Workspaces {
			dirs = append(dirs, w.Worktree.RepoRoot)
		}
	case "rex":
		// ponytail: a session's folder is its first window's focused block's, as Shepherd names a Rex workspace;
		// two calls a session, side by side
		out, err := run("", "rex", "list", "--json")
		if err != nil {
			return nil, err
		}
		var l struct {
			Sessions []struct {
				ID string `json:"session_id"`
			} `json:"sessions"`
		}
		if err := json.Unmarshal([]byte(out), &l); err != nil {
			return nil, err
		}
		dirs = make([]string, len(l.Sessions))
		var wg sync.WaitGroup
		for i, s := range l.Sessions {
			wg.Add(1)
			go func() {
				defer wg.Done()
				out, _ := run("", "rex", "session", "inspect", s.ID, "--json")
				if block := field(out, "windows[0].focused_block_id"); block != "" {
					out, _ := run("", "rex", "block", "call", "-s", s.ID, "-b", block, "process")
					dirs[i] = field(out, "foreground.cwd")
				}
			}()
		}
		wg.Wait()
	case "tmux":
		out, err := run("", "tmux", "list-panes", "-a", "-F", "#{pane_current_path}")
		if err != nil && (strings.Contains(err.Error(), "no server running") || strings.Contains(err.Error(), "error connecting")) {
			return nil, nil // no server: nothing open
		}
		if err != nil {
			return nil, err
		}
		dirs = strings.Split(out, "\n")
	case "hc":
		out, err := run("", "hc", "ls", "--json")
		if err != nil {
			return nil, err
		}
		var l struct {
			Terminals []struct {
				Cwd        string `json:"cwd"`
				Foreground struct {
					Cwd string `json:"cwd"`
				} `json:"foreground"`
			} `json:"terminals"`
		}
		if err := json.Unmarshal([]byte(out), &l); err != nil {
			return nil, err
		}
		for _, t := range l.Terminals {
			dirs = append(dirs, cmp.Or(t.Foreground.Cwd, t.Cwd))
		}
	default:
		return nil, fmt.Errorf("unknown backend %q", backend)
	}
	seen := map[string]bool{}
	list := []string{}
	for _, d := range dirs {
		if d = strings.TrimSpace(d); d == "" {
			continue
		}
		// the common git dir is the main checkout's .git, for a worktree too
		out, err := run(d, "git", "rev-parse", "--path-format=absolute", "--git-common-dir")
		root := filepath.Dir(strings.TrimSpace(out))
		if err != nil || filepath.Base(strings.TrimSpace(out)) != ".git" || seen[root] {
			continue
		}
		seen[root] = true
		list = append(list, root)
	}
	return list, nil
}
