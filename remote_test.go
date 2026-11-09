package main

import "testing"

func TestFromRemote(t *testing.T) {
	for in, want := range map[string]string{
		"git@github.com:getshepherd/shepherd.git":             "github.com/getshepherd/shepherd github",
		"https://github.com/getshepherd/shepherd":             "github.com/getshepherd/shepherd github",
		"ssh://git@ci.example.net:2222/callcenter/mono.git\n": "ci.example.net/callcenter/mono gitlab",
		"https://oauth2:tok@gitlab.com/a/b/c.git":             "gitlab.com/a/b/c gitlab",
	} {
		r, err := fromRemote(in)
		if got := r.key() + " " + r.Provider; err != nil || got != want {
			t.Errorf("fromRemote(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := fromRemote("/some/local/path"); err == nil {
		t.Error("a local path read as a remote")
	}
	for in, want := range map[string]string{"getshepherd/shepherd": "github.com/getshepherd/shepherd", "ci.example.net/g/sub/p": "ci.example.net/g/sub/p"} {
		if r, err := resolve(in); err != nil || r.key() != want {
			t.Errorf("resolve(%q) = %q, %v; want %q", in, r.key(), err, want)
		}
	}
}

func TestRollup(t *testing.T) {
	ok := check{Status: "COMPLETED", Conclusion: "SUCCESS"}
	for want, checks := range map[string][]check{
		"":        nil,
		"passing": {ok, {State: "SUCCESS"}},
		"pending": {ok, {Status: "IN_PROGRESS"}},
		"failing": {{Status: "IN_PROGRESS"}, {Status: "COMPLETED", Conclusion: "FAILURE"}, ok},
	} {
		if got := rollup(checks); got != want {
			t.Errorf("rollup(%v) = %q, want %q", checks, got, want)
		}
	}
}
