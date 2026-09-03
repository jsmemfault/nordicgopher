package github

import "testing"

func TestAffordableRepos(t *testing.T) {
	cases := []struct {
		budget, orgs, want int
		why                string
	}{
		// The failure this exists to prevent: the default of 15 repos per
		// organisation costs 62 requests against a 60-request budget, so a
		// first run on a cold cache could never succeed.
		{60, 2, 13, "a fresh unauthenticated budget"},
		{58, 2, 13, "after reading the rate limit"},
		{4, 2, 0, "nothing left to spend"},
		{0, 2, 0, "budget exhausted"},
		{6, 1, 0, "reserve and listing consume it"},
		{60, 1, 27, "a single organisation gets the whole budget"},
		{60, 0, 27, "no organisations is treated as one"},
	}
	for _, c := range cases {
		if got := affordableRepos(c.budget, c.orgs); got != c.want {
			t.Errorf("affordableRepos(%d, %d) = %d, want %d (%s)",
				c.budget, c.orgs, got, c.want, c.why)
		}
	}
}

func TestAffordableReposLeavesTheReserve(t *testing.T) {
	// The documentation ingest needs exactly one API request. If this
	// spends the whole budget, a deployment can mirror repositories but not
	// documentation, which is the larger half of the mirror.
	const budget, orgs = 60, 2
	spent := orgs + affordableRepos(budget, orgs)*orgs*requestsPerRepo
	if left := budget - spent; left < 1 {
		t.Errorf("spent %d of %d, leaving %d: the docs ingest needs at least one request",
			spent, budget, left)
	}
}

func TestRepoLimitLabel(t *testing.T) {
	if got := repoLimitLabel(0); got != "all" {
		t.Errorf("zero means no limit, got %q", got)
	}
	if got := repoLimitLabel(15); got != "15" {
		t.Errorf("got %q", got)
	}
}

func TestHumanBytes(t *testing.T) {
	for n, want := range map[int64]string{
		512:                    "512 B",
		1024:                   "1.0 KiB",
		5 * 1024 * 1024:        "5.0 MiB",
		3 * 1024 * 1024 * 1024: "3.0 GiB",
	} {
		if got := humanBytes(n); got != want {
			t.Errorf("humanBytes(%d) = %q, want %q", n, got, want)
		}
	}
}
