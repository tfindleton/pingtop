package pingtop

import (
	"os"
	"path/filepath"
	"regexp"
	"time"
)

const (
	configFilename = "pingtop.json"
	logFilename    = "pingtop_log.csv"
	snapshotPrefix = "pingtop_snapshot_"
)

var (
	goRunWorkPath  = regexp.MustCompile(`/go-build[0-9]+/b[0-9]+/exe/[^/]+$`)
	goRunCachePath = regexp.MustCompile(`/([0-9a-f]{2})/([0-9a-f]{64})-d/[^/]+$`)
)

type RuntimePaths struct {
	LaunchPath string
	RuntimeDir string
	ConfigPath string
	LogPath    string
}

func (paths RuntimePaths) SnapshotPath(timestamp time.Time) string {
	value := timestamp
	if value.IsZero() {
		value = time.Now()
	}
	return filepath.Join(paths.RuntimeDir, snapshotPrefix+value.Format("20060102_150405")+".txt")
}

func ResolveRuntimePaths() RuntimePaths {
	cwd, _ := os.Getwd()
	executable, _ := os.Executable()
	return resolveRuntimePathsFor(os.Args[0], cwd, executable)
}

func resolveRuntimePathsFor(argv0, cwd, executable string) RuntimePaths {
	launchPath := resolveLaunchPath(argv0, cwd, executable)
	runtimeDir := resolveRuntimeDir(launchPath, cwd)
	return RuntimePaths{
		LaunchPath: launchPath,
		RuntimeDir: runtimeDir,
		ConfigPath: filepath.Join(runtimeDir, configFilename),
		LogPath:    filepath.Join(runtimeDir, logFilename),
	}
}

func resolveLaunchPath(argv0, cwd, executable string) string {
	baseDir := cwd
	if baseDir == "" {
		baseDir = "."
	}
	// go run sets argv[0] to the same build or cache binary as os.Executable.
	// Falling back to argv[0] would still put runtime files in the build cache.
	if isGoRunExecutable(executable) || isGoRunExecutable(argv0) {
		absolute, _ := filepath.Abs(baseDir)
		return absolute
	}
	if executable != "" {
		if absolute, err := filepath.Abs(executable); err == nil {
			return absolute
		}
	}
	raw := argv0
	if raw == "" || raw == "-" || raw == "-c" || raw == "-m" {
		absolute, _ := filepath.Abs(baseDir)
		return absolute
	}
	candidate := raw
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(baseDir, candidate)
	}
	absolute, _ := filepath.Abs(candidate)
	return absolute
}

func resolveRuntimeDir(launchPath, cwd string) string {
	info, err := os.Stat(launchPath)
	if err == nil && info.IsDir() {
		return launchPath
	}
	if launchPath == "" {
		if cwd == "" {
			return "."
		}
		return cwd
	}
	return filepath.Dir(launchPath)
}

func isGoRunExecutable(path string) bool {
	if path == "" {
		return false
	}
	cleaned := filepath.ToSlash(filepath.Clean(path))
	if goRunWorkPath.MatchString(cleaned) {
		return true
	}
	// Recent Go versions can execute directly from the build cache. Match its
	// hash layout, including custom GOCACHE roots, rather than directory names.
	match := goRunCachePath.FindStringSubmatch(cleaned)
	return len(match) == 3 && match[1] == match[2][:2]
}
