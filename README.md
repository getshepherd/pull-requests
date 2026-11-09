# Pull requests

A Shepherd extension: the pull and merge requests of every repository you work in, on one page, after t3code's Pull
requests view. Built on [paddock](https://github.com/getshepherd/extensions): one Go binary with its page, a CLI and a
service.

- **Repositories**: those the backend's terminals are in (herdr's workspaces, Rex's sessions, tmux's panes, hc's
  terminals), a worktree as its repository, each by its `upstream` remote, else `origin`; and any added with
  `add-repo`. github.com is read with `gh`, every other host as GitLab with `glab` (one GraphQL read a project, line
  counts, pipeline and reviewers included).
- **The list**: grouped into Authored, Reviewing (your review asked for) and Others. Each row shows its state (open,
  draft, merged, closed; a red triangle on one that conflicts with its base), checks, review, author, labels and size.
  30 a repository at first, Load more for the next 30.
- **Filters**, as t3code's: state, involvement, author, labels, draft, review, checks, host and project; sorts; and a
  search that takes `label:`, `author:` (`author:me`), `repo:` and `is:draft|approved|mine` beside plain words.
- **Held Shift** shows Close and Merge (Ready for review on a draft) on every open row; Close and Merge ask again on
  the button itself.
- **A row** opens its PR in Shepherd's review panel, by a `shepherd://review?dir=…&branch=…` link: the panel reads it
  from the checkout as it reads a pane's branch. A repository added by URL has no checkout, so its rows open on the
  host.
- **Cache**: each listing is kept on disk and answered at once; one older than a minute is asked for again behind it,
  and the page takes the new one when it comes.

## Install

From Shepherd's Extensions, `getshepherd/pull-requests`. Or from a checkout:

```sh
go run . install --start shepherd
```

## CLI

```sh
pull-requests list [--state open|merged|closed|all] [--limit 30] [--refresh]
pull-requests repos
pull-requests add-repo --repo ~/code/app        # or git@host:group/app.git, or host/group/app, or owner/name
pull-requests remove-repo --repo host/group/app
pull-requests close|merge|ready --repo host/group/app --number 12
```

Not yet: GitHub Enterprise hosts (read as GitLab), review requests to a team you're on, more than 100 a GitLab
project.

Icons: [Lucide](https://lucide.dev) (ISC), the set t3code uses; logos from [Simple Icons](https://simpleicons.org)
(CC0).
