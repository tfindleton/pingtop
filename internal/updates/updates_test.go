package updates

import (
	"sync"
	"testing"
	"time"

	"github.com/tfindleton/pingtop/internal/pingtop"
)

func TestVersionHelpers(t *testing.T) {
	if _, _, _, ok := parseVersionTag("v" + pingtop.Version); !ok {
		t.Fatalf("expected version tag to parse: %s", pingtop.Version)
	}
	if _, _, _, ok := parseVersionTag(pingtop.Version); !ok {
		t.Fatalf("expected bare version to parse: %s", pingtop.Version)
	}
	if normalizeRepoURL("https://github.com/tfindleton/pingtop.git") != "https://github.com/tfindleton/pingtop" {
		t.Fatal("failed to normalize https repo url")
	}
	if normalizeRepoURL("git@github.com:tfindleton/pingtop.git") != "https://github.com/tfindleton/pingtop" {
		t.Fatal("failed to normalize ssh repo url")
	}
	if !isNewerVersion("v0.1.0", "v0.2.0") {
		t.Fatal("expected newer version comparison to succeed")
	}
	if !isNewerVersion("0.1.0", "0.2.0") {
		t.Fatal("expected bare newer version comparison to succeed")
	}
	if isNewerVersion("v0.2.0", "v0.1.0") {
		t.Fatal("expected older version comparison to fail")
	}
	if isNewerVersion("0.2.0", "0.1.0") {
		t.Fatal("expected older bare version comparison to fail")
	}
}

func TestBuildReleaseAPIURLRequiresGitHubRepo(t *testing.T) {
	url, err := buildReleaseAPIURL("https://github.com/tfindleton/pingtop")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected := "https://api.github.com/repos/tfindleton/pingtop/releases/latest"
	if url != expected {
		t.Fatalf("unexpected api url: %q", url)
	}
	if _, err := buildReleaseAPIURL("https://example.com/notgithub/repo"); err == nil {
		t.Fatal("expected non-github repo to fail")
	}
}

func TestUpdateManagerMarksAvailableVersion(t *testing.T) {
	manager := NewUpdateManager(
		"0.1.0",
		"https://github.com/tfindleton/pingtop",
		true,
		func(repoURL string, timeout time.Duration) (string, string, error) {
			return "0.2.0", "https://github.com/tfindleton/pingtop/releases/tag/0.2.0", nil
		},
	)
	manager.run()
	status := manager.Snapshot()
	if status.State != "available" || status.LatestVersion != "0.2.0" {
		t.Fatalf("unexpected update status: %#v", status)
	}
}

func TestUpdateManagerCheckNowReturnsSnapshot(t *testing.T) {
	manager := NewUpdateManager(
		"0.1.3",
		"https://github.com/tfindleton/pingtop",
		true,
		func(repoURL string, timeout time.Duration) (string, string, error) {
			return "0.1.4", "https://github.com/tfindleton/pingtop/releases/tag/0.1.4", nil
		},
	)
	status := manager.CheckNow()
	if status.State != "available" || status.LatestVersion != "0.1.4" {
		t.Fatalf("unexpected check-now status: %#v", status)
	}
}

func TestUpdateManagerRefreshesWhileRunning(t *testing.T) {
	var mu sync.Mutex
	latestVersion := "0.1.0"
	manager := NewUpdateManager(
		"0.1.0",
		"https://github.com/tfindleton/pingtop",
		true,
		func(repoURL string, timeout time.Duration) (string, string, error) {
			mu.Lock()
			defer mu.Unlock()
			return latestVersion, "https://github.com/tfindleton/pingtop/releases/tag/" + latestVersion, nil
		},
	)
	manager.pollInterval = 10 * time.Millisecond
	manager.Start()
	defer manager.Stop()

	deadline := time.Now().Add(250 * time.Millisecond)
	for time.Now().Before(deadline) {
		if manager.Snapshot().State == "current" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	mu.Lock()
	latestVersion = "0.2.0"
	mu.Unlock()

	deadline = time.Now().Add(250 * time.Millisecond)
	for time.Now().Before(deadline) {
		status := manager.Snapshot()
		if status.State == "available" && status.LatestVersion == "0.2.0" {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}

	t.Fatalf("expected periodic refresh to detect new release, got %#v", manager.Snapshot())
}
