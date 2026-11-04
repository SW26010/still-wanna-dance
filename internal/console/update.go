package console

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/mod/semver"

	"still-wanna-dance/internal/buildinfo"
)

const (
	releasesAPI     = "https://api.github.com/repos/SW26010/still-wanna-dance/releases?per_page=30"
	releasePageBase = "https://github.com/SW26010/still-wanna-dance/releases/tag/"
	updateTimeout   = 12 * time.Second
	maxReleaseBytes = 8 << 20
)

type updateStatus struct {
	Checking      bool   `json:"checking"`
	Result        string `json:"result"` // unknown, current or available
	Reason        string `json:"reason,omitempty"`
	CheckedAt     string `json:"checkedAt,omitempty"`
	LatestVersion string `json:"latestVersion,omitempty"`
	ReleaseURL    string `json:"releaseURL,omitempty"`
	StartupNotice bool   `json:"startupNotice,omitempty"`
}

// checkLatestRelease does not start a download or trust a URL from the response.
func checkLatestRelease(ctx context.Context, client *http.Client, build buildinfo.Info) updateStatus {
	result := updateStatus{Result: "unknown", Reason: "failed"}
	type release struct {
		Tag        string `json:"tag_name"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
	}
	var latest string
	for page := 1; ; page++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, releasesAPI+"&page="+strconv.Itoa(page), nil)
		if err != nil {
			return result
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("User-Agent", "Still-Wanna-Dance")
		resp, err := client.Do(req)
		if err != nil {
			return result
		}
		if resp.StatusCode == http.StatusNotFound {
			resp.Body.Close()
			result.Reason = "not_found"
			return result
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return result
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxReleaseBytes+1))
		resp.Body.Close()
		if err != nil || len(body) > maxReleaseBytes {
			return result
		}
		var releases []release
		if json.Unmarshal(body, &releases) != nil || releases == nil {
			return result
		}
		for _, candidate := range releases {
			if candidate.Draft || candidate.Prerelease || !semver.IsValid(candidate.Tag) ||
				semver.Canonical(candidate.Tag) != candidate.Tag || semver.Prerelease(candidate.Tag) != "" {
				continue
			}
			if latest == "" || semver.Compare(candidate.Tag, latest) > 0 {
				latest = candidate.Tag
			}
		}
		// Follow GitHub's pagination signal but construct the next URL locally.
		if !strings.Contains(resp.Header.Get("Link"), `rel="next"`) {
			break
		}
	}
	if latest == "" {
		result.Reason = "not_found"
		return result
	}
	if build.Kind == "development" || !semver.IsValid(build.Version) {
		result.Reason = "version"
		return result
	}
	result.Reason = ""
	result.Result = "current"
	if semver.Compare(latest, build.Version) > 0 {
		result.Result = "available"
		result.LatestVersion = latest
		result.ReleaseURL = releasePageBase + url.PathEscape(latest)
	}
	return result
}

// StartUpdateCheck requests exactly one automatic check for this process. If
// consent is pending, acceptTerms starts it after the active network is ready.
func (c *Console) StartUpdateCheck() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.updateAutoRequested = true
	c.startPendingUpdateCheckLocked()
}

func (c *Console) startPendingUpdateCheckLocked() {
	if !c.updateAutoRequested || c.updateAutoStarted || c.closing || !c.terms.valid() || c.client == nil {
		return
	}
	c.updateAutoStarted = true
	c.startUpdateCheckLocked(true)
}

// Caller holds mu. Concurrent manual requests share the in-flight check.
func (c *Console) startUpdateCheckLocked(automatic bool) <-chan struct{} {
	if c.updateDone != nil {
		return c.updateDone
	}
	ctx, cancel := context.WithTimeout(context.Background(), updateTimeout)
	done := make(chan struct{})
	client, build := c.client, buildinfo.Current()
	c.update.Checking = true
	c.updateCancel, c.updateDone = cancel, done
	go func() {
		defer cancel()
		result := checkLatestRelease(ctx, client, build)
		result.CheckedAt = time.Now().UTC().Format(time.RFC3339Nano)
		c.mu.Lock()
		if !c.closing {
			c.update = result
			if automatic && result.Result == "available" {
				c.updateNotice = result.LatestVersion
			}
		}
		c.updateCancel, c.updateDone = nil, nil
		close(done)
		c.mu.Unlock()
	}()
	return done
}

func (c *Console) updateSnapshotLocked() updateStatus {
	result := c.update
	if result.Result == "" {
		result.Result = "unknown"
	}
	result.StartupNotice = c.updateNotice != ""
	return result
}

func (c *Console) serveUpdateCheck(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	if c.closing || c.client == nil {
		c.mu.Unlock()
		http.Error(w, "检查暂不可用", http.StatusServiceUnavailable)
		return
	}
	done := c.startUpdateCheckLocked(false)
	c.mu.Unlock()
	select {
	case <-done:
		c.mu.Lock()
		result := c.updateSnapshotLocked()
		c.mu.Unlock()
		writeJSON(w, result)
	case <-r.Context().Done():
	}
}

// Claim the automatic notice once across all WebUI tabs and page reloads.
func (c *Console) serveUpdateNotice(w http.ResponseWriter) {
	c.mu.Lock()
	version := c.updateNotice
	c.updateNotice = ""
	c.mu.Unlock()
	writeJSON(w, struct {
		Version string `json:"version,omitempty"`
	}{version})
}
