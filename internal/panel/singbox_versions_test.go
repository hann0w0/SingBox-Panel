package panel

import "testing"

func TestCompareSemver(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		// equal
		{"1.14.0", "1.14.0", 0},
		{"1.14.0-rc.1", "1.14.0-rc.1", 0},
		// a < b
		{"1.13.0", "1.14.0", -1},
		{"1.14.0", "1.14.1", -1},
		{"1.9.0", "1.10.0", -1},
		{"1.14.0-rc.1", "1.14.0", -1}, // pre-release < release
		// a > b
		{"1.15.0", "1.14.0", 1},
		{"1.14.1", "1.14.0", 1},
		{"1.10.0", "1.9.0", 1},
		{"1.14.0", "1.14.0-rc.1", 1}, // release > pre-release
	}
	for _, tt := range tests {
		got := compareSemver(tt.a, tt.b)
		if got != tt.want {
			t.Errorf("compareSemver(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestCheckSingboxUpdate_InstalledIsNewer(t *testing.T) {
	// When installed version is newer than cached latest,
	// should NOT report an update.
	releases := SingboxLatestReleases{Stable: "1.14.0", Beta: "1.15.0-rc.1"}

	// Installed 1.15.0 > cached stable 1.14.0 → no update
	hasUp, latest := checkSingboxUpdate("1.15.0", releases)
	if hasUp {
		t.Errorf("1.15.0 vs stable=1.14.0: hasUpdate=%v, latest=%q — should be false", hasUp, latest)
	}

	// Installed 1.14.0 == cached stable 1.14.0 → no update
	hasUp, latest = checkSingboxUpdate("1.14.0", releases)
	if hasUp {
		t.Errorf("1.14.0 vs stable=1.14.0: hasUpdate=%v, latest=%q — should be false", hasUp, latest)
	}

	// Installed 1.13.0 < cached stable 1.14.0 → has update
	hasUp, latest = checkSingboxUpdate("1.13.0", releases)
	if !hasUp {
		t.Errorf("1.13.0 vs stable=1.14.0: hasUpdate=%v — should be true", hasUp)
	}
	if latest != "1.14.0" {
		t.Errorf("latest should be 1.14.0, got %q", latest)
	}
}

func TestCheckSingboxUpdate_BetaIsNewer(t *testing.T) {
	releases := SingboxLatestReleases{Stable: "1.14.0", Beta: "1.15.0-beta.1"}

	// Installed beta 1.15.0-beta.1 == cached beta 1.15.0-beta.1 → no update
	hasUp, _ := checkSingboxUpdate("1.15.0-beta.1", releases)
	if hasUp {
		t.Error("same beta version should not report update")
	}

	// Installed beta 1.15.0-beta.2 > cached beta 1.15.0-beta.1 → no update
	hasUp, _ = checkSingboxUpdate("1.15.0-beta.2", releases)
	if hasUp {
		t.Error("newer beta version should not report update")
	}
}