package updates

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
)

var versionTagRE = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)$`)

const defaultUpdateCheckInterval = 5 * time.Minute

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
		return "check failed"
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
	if strings.HasSuffix(value, ".git") {
		value = strings.TrimSuffix(value, ".git")
	}
	return strings.TrimRight(value, "/")
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

func buildReleaseAPIURL(repoURL string) (string, error) {
	normalized := normalizeRepoURL(repoURL)
	parsed, err := url.Parse(normalized)
	if err != nil {
		return "", err
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" || !strings.EqualFold(parsed.Host, "github.com") {
		return "", errors.New("update repo URL must be a GitHub repository URL")
	}
	parts := strings.FieldsFunc(parsed.Path, func(r rune) bool { return r == '/' })
	if len(parts) < 2 {
		return "", errors.New("update repo URL must include owner and repository name")
	}
	return fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", parts[0], parts[1]), nil
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

	client := &http.Client{Timeout: timeout}
	response, err := client.Do(request)
	if err != nil {
		return "", "", err
	}
	defer response.Body.Close()

	var payload struct {
		TagName string `json:"tag_name"`
		HTMLURL string `json:"html_url"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return "", "", err
	}
	if _, _, _, ok := parseVersionTag(payload.TagName); !ok {
		return "", "", errors.New("latest GitHub release did not contain a semantic version tag")
	}
	htmlURL := strings.TrimSpace(payload.HTMLURL)
	if htmlURL == "" {
		htmlURL = normalizeRepoURL(repoURL) + "/releases"
	}
	return strings.TrimSpace(payload.TagName), htmlURL, nil
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
		return false, fmt.Sprintf("Unable to open browser for %s", targetURL)
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

	latestVersion, releaseURL, err := manager.fetcher(manager.repoURL, 3*time.Second)
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
	var command []string
	switch runtime.GOOS {
	case "windows":
		command = []string{"rundll32", "url.dll,FileProtocolHandler", targetURL}
	case "darwin":
		command = []string{"open", targetURL}
	default:
		command = []string{"xdg-open", targetURL}
	}
	cmd := exec.Command(command[0], command[1:]...)
	return cmd.Start()
}
