package panel

import (
	"regexp"
	"strings"
)

var agentVersionPattern = regexp.MustCompile(`^v?[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z][0-9A-Za-z.-]*)?$`)

// compareAgentVersions compares two published Agent versions. Unknown or
// development labels are left unordered so they can still be repaired by hash.
func compareAgentVersions(current, available string) (int, bool) {
	current = strings.TrimSpace(current)
	available = strings.TrimSpace(available)
	if !agentVersionPattern.MatchString(current) || !agentVersionPattern.MatchString(available) {
		return 0, false
	}
	return compareSemver(strings.TrimPrefix(current, "v"), strings.TrimPrefix(available, "v")), true
}

func agentHasUpdate(current, available string) bool {
	comparison, known := compareAgentVersions(current, available)
	return known && comparison != 0
}
