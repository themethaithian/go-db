package app

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// latestReleaseURL is GitHub's "latest release" API — the newest release
// that isn't a draft or prerelease, which is exactly what a human clicking
// "download go-db" would land on too.
const latestReleaseURL = "https://api.github.com/repos/themethaithian/go-db/releases/latest"

// updateCheckTimeout bounds CheckForUpdate's network call. The check runs
// silently on app startup and every 24h after — it must never make the app
// feel like it's waiting on the network, so a slow or hung connection gives
// up quickly rather than stalling anything.
const updateCheckTimeout = 3 * time.Second

// UpdateInfo is what CheckForUpdate reports to the frontend: the running
// version, the latest published release (if reachable), and whether the
// latter is newer. Latest and URL are empty when the check couldn't
// complete — Outdated is then always false, so the frontend never needs to
// distinguish "checked, up to date" from "couldn't check".
type UpdateInfo struct {
	Current  string `json:"current"`
	Latest   string `json:"latest"`
	URL      string `json:"url"`
	Outdated bool   `json:"outdated"`
}

// CheckForUpdate asks GitHub for go-db's latest release and compares it
// against the running build. It never returns an error to the frontend: a
// timeout, an offline machine, a rate limit, or a malformed response all
// come back as Outdated:false with an empty Latest — the update check is a
// courtesy, not something that should ever demand the human's attention on
// its own, let alone surface an error dialog for a feature nobody asked to
// see fail.
func (a *App) CheckForUpdate() UpdateInfo {
	info := UpdateInfo{Current: a.version}

	req, err := http.NewRequest(http.MethodGet, latestReleaseURL, nil)
	if err != nil {
		return info
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	client := &http.Client{Timeout: updateCheckTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return info
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return info
	}

	var payload struct {
		TagName string `json:"tag_name"`
		HTMLURL string `json:"html_url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return info
	}

	info.Latest = payload.TagName
	info.URL = payload.HTMLURL
	info.Outdated = isOutdated(a.version, payload.TagName)
	return info
}

// isOutdated reports whether latest names a newer release than current.
// Both are semver-ish tags ("v1.2.3" or "1.2.3", an optional leading "v",
// any number of dot-separated numeric fields). "dev" — the unstamped
// default every local build reports — is never outdated, and neither is an
// unparseable current or latest: with nothing safe to compare against, the
// honest answer is "don't nag", not a guess.
func isOutdated(current, latest string) bool {
	if current == "dev" {
		return false
	}
	c, ok := parseVersion(current)
	if !ok {
		return false
	}
	l, ok := parseVersion(latest)
	if !ok {
		return false
	}

	n := len(c)
	if len(l) > n {
		n = len(l)
	}
	for i := 0; i < n; i++ {
		var cv, lv int
		if i < len(c) {
			cv = c[i]
		}
		if i < len(l) {
			lv = l[i]
		}
		if lv != cv {
			return lv > cv
		}
	}
	return false
}

// parseVersion splits a semver-ish tag into numeric fields, stripping a
// leading "v" and any prerelease/build suffix ("-rc1", "+build5"). It
// reports ok=false for anything that isn't purely dot-separated integers —
// callers treat that as "can't compare" rather than guessing.
func parseVersion(v string) ([]int, bool) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if v == "" {
		return nil, false
	}
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	fields := strings.Split(v, ".")
	nums := make([]int, len(fields))
	for i, f := range fields {
		n, err := strconv.Atoi(f)
		if err != nil {
			return nil, false
		}
		nums[i] = n
	}
	return nums, true
}

// OpenURL opens url in the OS default browser — the update chip's
// click-through onto the GitHub release page. Only http(s) URLs are
// accepted; anything else is a no-op, so a binding this permissive can't be
// repurposed to launch arbitrary local files or custom URL schemes.
func (a *App) OpenURL(url string) {
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return
	}
	runtime.BrowserOpenURL(a.ctx, url)
}
