package panel

import "testing"

func TestCompareAgentVersions(t *testing.T) {
	cases := []struct {
		current    string
		available  string
		comparison int
		known      bool
	}{
		{current: "v1.1.0", available: "v1.1.1", comparison: -1, known: true},
		{current: "1.1.1", available: "v1.1.1", comparison: 0, known: true},
		{current: "v1.1.2", available: "v1.1.1", comparison: 1, known: true},
		{current: "development", available: "v1.1.1", comparison: 0, known: false},
		{current: "", available: "v1.1.1", comparison: 0, known: false},
	}
	for _, tc := range cases {
		comparison, known := compareAgentVersions(tc.current, tc.available)
		if comparison != tc.comparison || known != tc.known {
			t.Fatalf("compareAgentVersions(%q, %q) = (%d, %v), want (%d, %v)",
				tc.current, tc.available, comparison, known, tc.comparison, tc.known)
		}
	}
}

func TestAgentVersionMismatch(t *testing.T) {
	if !agentHasUpdate("v1.1.0", "v1.1.1") {
		t.Fatal("older Agent was not marked for update")
	}
	if !agentHasUpdate("v1.1.2", "v1.1.1") {
		t.Fatal("newer-labelled Agent was not marked for synchronization")
	}
	if agentHasUpdate("v1.1.1", "v1.1.1") {
		t.Fatal("matching Agent version was marked for synchronization")
	}
}
