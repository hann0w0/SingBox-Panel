package panel

import "strings"

// baseURL is the public origin used for subscription links and the agent
// install command. It is set via the WEB (or BASE_URL) env var — see
// config.applyEnv.
func (a *App) baseURL() string {
	return strings.TrimRight(a.cfg.BaseURL, "/")
}
