package approvals

import "testing"

func TestContainsPhraseBoundaries(t *testing.T) {
	cases := []struct {
		name   string
		text   string
		phrase string
		want   bool
	}{
		{name: "word", text: "git\npush origin main", phrase: "main", want: true},
		{name: "inside remaining", text: "the remaining work", phrase: "main", want: false},
		{name: "inside domain", text: "domain", phrase: "main", want: false},
		{name: "case", text: "Push origin MAIN", phrase: "main", want: true},
		{name: "phrase", text: "I am Deleting Data now", phrase: "deleting data", want: true},
		{name: "phrase prefix", text: "deleting database", phrase: "deleting data", want: false},
		{name: "empty", text: "push main", phrase: "  ", want: false},
		{name: "punctuation", text: `push {"branch":"main"}`, phrase: "main", want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := containsPhrase(tc.text, tc.phrase); got != tc.want {
				t.Fatalf("got %v", got)
			}
		})
	}
}

func TestPushAndCustomMatchers(t *testing.T) {
	raw := []byte(`[{"kind":"push_protected","label":""},{"kind":"push_protected","label":"release"},{"kind":"custom","label":"delete the database"},{"kind":"custom","label":""}]`)
	push := pushMatchesFor(raw, "git\npush origin main")
	if len(push) != 1 || push[0].Branch != "main" {
		t.Fatalf("push=%+v", push)
	}
	release := pushMatchesFor(raw, "git\npush origin release")
	if len(release) != 1 || release[0].Branch != "release" {
		t.Fatalf("release=%+v", release)
	}
	if got := pushMatchesFor(raw, "status of main"); len(got) != 0 {
		t.Fatalf("no push verb matched %+v", got)
	}
	custom := customMatchesText(raw, "Please delete the database tonight")
	if len(custom) != 1 || custom[0].Label != "delete the database" {
		t.Fatalf("custom=%+v", custom)
	}
	if got := customMatchesText(raw, "delete the datab"); len(got) != 0 {
		t.Fatalf("partial custom %+v", got)
	}
}

func TestHostAllowed(t *testing.T) {
	if !hostAllowed("open", nil, "evil.example") {
		t.Fatal("open policy")
	}
	if !hostAllowed("allowlist", []string{"docs.example.com", "*.internal.test"}, "a.internal.test") {
		t.Fatal("wildcard")
	}
	if hostAllowed("allowlist", []string{"docs.example.com"}, "evil.example.com") {
		t.Fatal("unlisted host")
	}
	if host, ok := normalizeHost(" Evil.Example.COM "); !ok || host != "evil.example.com" {
		t.Fatalf("host=%q ok=%v", host, ok)
	}
	if _, ok := normalizeHost("user@evil.example.com"); ok {
		t.Fatal("userinfo")
	}
}
