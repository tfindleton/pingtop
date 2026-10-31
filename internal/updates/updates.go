package updates

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

var versionTagRE = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)$`)
var repositoryPartRE = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

const (
	defaultUpdateCheckInterval  = 5 * time.Minute
	defaultUpdateRequestTimeout = 10 * time.Second
	maxReleaseResponseBytes     = 1 << 20
)

type UpdateStatus struct {
	State          string
	CurrentVersion string
	LatestVersion  string
	RepoURL        string
	ReleaseURL     string
	ErrorMessage   string
}

func (status UpdateStatus) IsAvailable() bool {
	return status.State == "available"
}

func (status UpdateStatus) Summary() string {
	switch status.State {
	case "disabled":
		return "disabled"
	case "checking":
		return "checking"
	case "available":
		if status.LatestVersion != "" {
			return status.LatestVersion + " available"
		}
		return "available"
	case "current":
		return "current"
	case "error":
		return "unavailable"
	default:
		return "-"
	}
}

func normalizeRepoURL(rawURL string) string {
	value := strings.TrimSpace(rawURL)
	if value == "" {
		return ""
	}
	if strings.HasPrefix(value, "git@github.com:") {
		value = "https://github.com/" + strings.TrimPrefix(value, "git@github.com:")
	}
	value = strings.TrimRight(value, "/")
	if strings.HasSuffix(value, ".git") {
		value = strings.TrimSuffix(value, ".git")
	}
	return value
}

func parseVersionTag(tag string) (major, minor, patch int, ok bool) {
	match := versionTagRE.FindStringSubmatch(strings.TrimSpace(tag))
	if len(match) != 4 {
		return 0, 0, 0, false
	}
	_, err1 := fmt.Sscanf(match[1], "%d", &major)
	_, err2 := fmt.Sscanf(match[2], "%d", &minor)
	_, err3 := fmt.Sscanf(match[3], "%d", &patch)
	if err1 != nil || err2 != nil || err3 != nil {
		return 0, 0, 0, false
	}
	return major, minor, patch, true
}

func isNewerVersion(currentVersion, latestVersion string) bool {
	currentMajor, currentMinor, currentPatch, okCurrent := parseVersionTag(currentVersion)
	latestMajor, latestMinor, latestPatch, okLatest := parseVersionTag(latestVersion)
	if !okCurrent || !okLatest {
		return false
	}
	if latestMajor != currentMajor {
		return latestMajor > currentMajor
	}
	if latestMinor != currentMinor {
		return latestMinor > currentMinor
	}
	return latestPatch > currentPatch
}

// Only accept canonical GitHub paths. In particular, reject credentials,
// encoded separators, dot segments, and non-HTTPS URL handlers.
func githubURLParts(rawURL string) ([]string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" || !strings.EqualFold(parsed.Host, "github.com") ||
		parsed.User != nil || parsed.Opaque != "" || parsed.RawQuery != "" || parsed.ForceQuery ||
		parsed.Fragment != "" || parsed.RawPath != "" {
		return nil, errors.New("update URL must be an HTTPS GitHub repository or release URL")
	}
	parts := strings.Split(strings.TrimPrefix(parsed.Path, "/"), "/")
	if len(parts) < 2 {
		return nil, errors.New("update URL must include owner and repository name")
	}
	for _, part := range parts {
		if !repositoryPartRE.MatchString(part) || part == "." || part == ".." {
			return nil, errors.New("update URL contains an invalid path")
		}
	}
	return parts, nil
}

func buildReleaseAPIURL(repoURL string) (string, error) {
	parts, err := githubURLParts(normalizeRepoURL(repoURL))
	if err != nil {
		return "", err
	}
	if len(parts) != 2 {
		return "", errors.New("update repo URL must contain only owner and repository name")
	}
	return fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", parts[0], parts[1]), nil
}

func checkUpdateRedirect(request *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("too many update redirects")
	}
	if request.URL.Scheme != "https" || !strings.EqualFold(request.URL.Host, "api.github.com") || request.URL.User != nil {
		return errors.New("update redirect must stay on https://api.github.com")
	}
	return nil
}

func fetchLatestRelease(repoURL string, timeout time.Duration) (string, string, error) {
	apiURL, err := buildReleaseAPIURL(repoURL)
	if err != nil {
		return "", "", err
	}
	request, err := http.NewRequest(http.MethodGet, apiURL, nil)
	if err != nil {
		return "", "", err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "pingtop-update-check")

	if timeout <= 0 {
		timeout = defaultUpdateRequestTimeout
	}
	client := &http.Client{Timeout: timeout, CheckRedirect: checkUpdateRedirect}
	response, err := client.Do(request)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return "", "", fmt.Errorf("update check timed out after %s", timeout)
		}
		return "", "", err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return "", "", errors.New("no GitHub releases found for update checks")
	}
	if response.StatusCode == http.StatusForbidden || response.StatusCode == http.StatusTooManyRequests {
		return "", "", fmt.Errorf("GitHub API rate limited update checks (HTTP %d)", response.StatusCode)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", "", fmt.Errorf("GitHub update check returned HTTP %d", response.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(response.Body, maxReleaseResponseBytes+1))
	if err != nil {
		return "", "", err
	}
	if len(data) > maxReleaseResponseBytes {
		return "", "", errors.New("GitHub release response exceeded size limit")
	}
	var payload struct {
		TagName string `json:"tag_name"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return "", "", err
	}
	if _, _, _, ok := parseVersionTag(payload.TagName); !ok {
		return "", "", errors.New("latest GitHub release did not contain a semantic version tag")
	}
	// Build the link from validated inputs instead of passing a response's
	// html_url to the operating system's URL handler.
	tag := strings.TrimSpace(payload.TagName)
	parts, _ := githubURLParts(normalizeRepoURL(repoURL)) // Validated above.
	releaseURL := fmt.Sprintf("https://github.com/%s/%s/releases/tag/%s", parts[0], parts[1], tag)
	return tag, releaseURL, nil
}

type releaseFetcher func(repoURL string, timeout time.Duration) (string, string, error)

type UpdateManager struct {
	currentVersion string
	repoURL        string
	enabled        bool
	fetcher        releaseFetcher
	pollInterval   time.Duration
	mu             sync.RWMutex
	status         UpdateStatus
	started        bool
	stopCh         chan struct{}
}

func NewUpdateManager(currentVersion, repoURL string, enabled bool, fetcher releaseFetcher) *UpdateManager {
	if fetcher == nil {
		fetcher = fetchLatestRelease
	}
	normalizedRepoURL := normalizeRepoURL(repoURL)
	active := enabled && normalizedRepoURL != ""
	initialState := "disabled"
	if active {
		initialState = "checking"
	}
	return &UpdateManager{
		currentVersion: currentVersion,
		repoURL:        normalizedRepoURL,
		enabled:        active,
		fetcher:        fetcher,
		pollInterval:   defaultUpdateCheckInterval,
		stopCh:         make(chan struct{}),
		status: UpdateStatus{
			State:          initialState,
			CurrentVersion: currentVersion,
			RepoURL:        normalizedRepoURL,
			ReleaseURL:     normalizedRepoURL + "/releases",
		},
	}
}

func (manager *UpdateManager) Start() {
	manager.mu.Lock()
	if !manager.enabled || manager.started {
		manager.mu.Unlock()
		return
	}
	manager.started = true
	stopCh := manager.stopCh
	pollInterval := manager.pollInterval
	manager.mu.Unlock()

	go manager.runLoop(stopCh, pollInterval)
}

func (manager *UpdateManager) Stop() {
	manager.mu.Lock()
	if manager.stopCh == nil {
		manager.mu.Unlock()
		return
	}
	stopCh := manager.stopCh
	manager.stopCh = nil
	manager.mu.Unlock()

	close(stopCh)
}

func (manager *UpdateManager) Snapshot() UpdateStatus {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	return manager.status
}

func (manager *UpdateManager) CheckNow() UpdateStatus {
	if !manager.enabled {
		return manager.Snapshot()
	}
	manager.run()
	return manager.Snapshot()
}

func (manager *UpdateManager) OpenPage() (bool, string) {
	status := manager.Snapshot()
	targetURL := status.ReleaseURL
	if targetURL == "" {
		targetURL = status.RepoURL
	}
	if targetURL == "" {
		return false, "No project URL configured for update checks"
	}
	if err := openBrowser(targetURL); err != nil {
		return false, fmt.Sprintf("Unable to open browser: %v", err)
	}
	return true, targetURL
}

func (manager *UpdateManager) runLoop(stopCh <-chan struct{}, pollInterval time.Duration) {
	manager.run()
	if pollInterval <= 0 {
		return
	}

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-stopCh:
			return
		case <-ticker.C:
			manager.run()
		}
	}
}

func (manager *UpdateManager) run() {
	nextStatus := UpdateStatus{
		State:          "current",
		CurrentVersion: manager.currentVersion,
		RepoURL:        manager.repoURL,
		ReleaseURL:     manager.repoURL + "/releases",
	}

	latestVersion, releaseURL, err := manager.fetcher(manager.repoURL, defaultUpdateRequestTimeout)
	if err != nil {
		nextStatus = UpdateStatus{
			State:          "error",
			CurrentVersion: manager.currentVersion,
			RepoURL:        manager.repoURL,
			ReleaseURL:     manager.repoURL + "/releases",
			ErrorMessage:   err.Error(),
		}
	} else if isNewerVersion(manager.currentVersion, latestVersion) {
		nextStatus = UpdateStatus{
			State:          "available",
			CurrentVersion: manager.currentVersion,
			LatestVersion:  latestVersion,
			RepoURL:        manager.repoURL,
			ReleaseURL:     releaseURL,
		}
	} else {
		nextStatus = UpdateStatus{
			State:          "current",
			CurrentVersion: manager.currentVersion,
			LatestVersion:  latestVersion,
			RepoURL:        manager.repoURL,
			ReleaseURL:     releaseURL,
		}
	}

	manager.mu.Lock()
	manager.status = nextStatus
	manager.mu.Unlock()
}

func openBrowser(targetURL string) error {
	parts, err := githubURLParts(targetURL)
	if err != nil {
		return err
	}
	validPath := len(parts) == 2 || (len(parts) == 3 && parts[2] == "releases")
	if len(parts) == 5 && parts[2] == "releases" && parts[3] == "tag" {
		_, _, _, validPath = parseVersionTag(parts[4])
	}
	if !validPath {
		return errors.New("update URL must point to a GitHub repository or release")
	}
	return launchBrowser(targetURL)
}
