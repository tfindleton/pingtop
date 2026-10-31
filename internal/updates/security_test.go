package updates

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func stubUpdateTransport(t *testing.T, transport roundTripFunc) {
	t.Helper()
	previous := http.DefaultTransport
	http.DefaultTransport = transport
	t.Cleanup(func() { http.DefaultTransport = previous })
}

func TestBuildReleaseAPIURLValidatesRepositoryPath(t *testing.T) {
	for _, input := range []string{
		"https://github.com/owner/repo",
		"https://GITHUB.COM/owner/repo/",
		"https://github.com/owner/repo.git/",
		"git@github.com:owner/repo.git",
	} {
		t.Run(input, func(t *testing.T) {
			got, err := buildReleaseAPIURL(input)
			if err != nil || got != "https://api.github.com/repos/owner/repo/releases/latest" {
				t.Fatalf("unexpected API URL %q, error %v", got, err)
			}
		})
	}
	for _, input := range []string{
		"http://github.com/owner/repo",
		"https://github.com.evil.example/owner/repo",
		"https://github.com:443/owner/repo",
		"https://user:secret@github.com/owner/repo",
		"https://github.com/owner/repo?query=1",
		"https://github.com/owner/repo?",
		"https://github.com/owner/repo#fragment",
		"https://github.com/owner/repo/releases",
		"https://github.com//owner/repo",
		"https://github.com/owner/..",
		"https://github.com/owner/%2e%2e",
		"https://github.com/owner/repo%2fother",
		"https://github.com/owner/repo%3fother",
		"https://github.com/owner/repo%0a",
		"https://github.com/owner",
		"https://github.com/owner/repo\\other",
	} {
		t.Run(input, func(t *testing.T) {
			if got, err := buildReleaseAPIURL(input); err == nil {
				t.Fatalf("accepted unsafe repository URL as %q", got)
			}
		})
	}
}

func TestFetchLatestReleaseConstructsTrustedLink(t *testing.T) {
	for _, htmlURL := range []string{
		"https://github.com/owner/repo/releases/tag/v1.2.3",
		"file:///tmp/evil.exe",
		"javascript:alert(1)",
		"https://evil.example/payload",
		"",
	} {
		t.Run(htmlURL, func(t *testing.T) {
			stubUpdateTransport(t, func(request *http.Request) (*http.Response, error) {
				if request.URL.String() != "https://api.github.com/repos/owner/repo/releases/latest" {
					t.Fatalf("unexpected request URL: %s", request.URL)
				}
				if request.Header.Get("Accept") != "application/vnd.github+json" || request.Header.Get("User-Agent") == "" {
					t.Fatal("missing GitHub request headers")
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(`{"tag_name":"v1.2.3","html_url":"` + htmlURL + `"}`)),
				}, nil
			})
			tag, link, err := fetchLatestRelease("git@github.com:owner/repo.git", time.Second)
			if err != nil || tag != "v1.2.3" || link != "https://github.com/owner/repo/releases/tag/v1.2.3" {
				t.Fatalf("unexpected release: %q, %q, %v", tag, link, err)
			}
		})
	}
}

type trackedBody struct {
	io.Reader
	read   int
	closed bool
}

func (body *trackedBody) Read(buffer []byte) (int, error) {
	n, err := body.Reader.Read(buffer)
	body.read += n
	return n, err
}

func (body *trackedBody) Close() error {
	body.closed = true
	return nil
}

func TestFetchLatestReleaseBoundsResponseAndClosesBody(t *testing.T) {
	valid := `{"tag_name":"1.2.3"}`
	for _, test := range []struct {
		name    string
		payload string
		wantErr bool
	}{
		{"at limit", valid + strings.Repeat(" ", maxReleaseResponseBytes-len(valid)), false},
		{"over limit", valid + strings.Repeat(" ", 2*maxReleaseResponseBytes), true},
		{"invalid JSON", `{`, true},
		{"trailing JSON", valid + valid, true},
		{"invalid tag", `{"tag_name":"../../evil"}`, true},
		{"missing tag", `{}`, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := &trackedBody{Reader: strings.NewReader(test.payload)}
			stubUpdateTransport(t, func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Body: body}, nil
			})
			_, _, err := fetchLatestRelease("https://github.com/owner/repo", time.Second)
			if (err != nil) != test.wantErr {
				t.Fatalf("unexpected error: %v", err)
			}
			if !body.closed || body.read > maxReleaseResponseBytes+1 {
				t.Fatalf("response not bounded/closed: read=%d closed=%v", body.read, body.closed)
			}
		})
	}
}

func TestFetchLatestReleaseRestrictsRedirects(t *testing.T) {
	for _, destination := range []string{
		"http://api.github.com/repos/owner/repo/releases/latest",
		"https://evil.example/release",
		"https://api.github.com.evil.example/release",
		"https://api.github.com:444/release",
		"https://user:password@api.github.com/release",
		"https://127.0.0.1/release",
	} {
		t.Run(destination, func(t *testing.T) {
			calls := 0
			stubUpdateTransport(t, func(*http.Request) (*http.Response, error) {
				calls++
				return &http.Response{
					StatusCode: http.StatusFound,
					Header:     http.Header{"Location": []string{destination}},
					Body:       io.NopCloser(strings.NewReader("")),
				}, nil
			})
			if _, _, err := fetchLatestRelease("https://github.com/owner/repo", time.Second); err == nil || calls != 1 {
				t.Fatalf("unsafe redirect followed: calls=%d error=%v", calls, err)
			}
		})
	}
	t.Run("repository rename", func(t *testing.T) {
		calls := 0
		stubUpdateTransport(t, func(*http.Request) (*http.Response, error) {
			calls++
			if calls == 1 {
				return &http.Response{
					StatusCode: http.StatusMovedPermanently,
					Header:     http.Header{"Location": []string{"https://api.github.com/repositories/123/releases/latest"}},
					Body:       io.NopCloser(strings.NewReader("")),
				}, nil
			}
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"tag_name":"1.2.3"}`))}, nil
		})
		if _, _, err := fetchLatestRelease("https://github.com/owner/repo", time.Second); err != nil || calls != 2 {
			t.Fatalf("safe redirect failed: calls=%d error=%v", calls, err)
		}
	})
	t.Run("redirect loop", func(t *testing.T) {
		calls := 0
		stubUpdateTransport(t, func(*http.Request) (*http.Response, error) {
			calls++
			return &http.Response{
				StatusCode: http.StatusFound,
				Header:     http.Header{"Location": []string{"https://api.github.com/loop"}},
				Body:       io.NopCloser(strings.NewReader("")),
			}, nil
		})
		if _, _, err := fetchLatestRelease("https://github.com/owner/repo", time.Second); err == nil || calls != 10 {
			t.Fatalf("redirect loop not bounded: calls=%d error=%v", calls, err)
		}
	})
}

func TestFetchLatestReleaseHandlesHTTPFailures(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusForbidden, http.StatusTooManyRequests, http.StatusInternalServerError} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			body := &trackedBody{Reader: strings.NewReader("")}
			stubUpdateTransport(t, func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: status, Body: body}, nil
			})
			if _, _, err := fetchLatestRelease("https://github.com/owner/repo", time.Second); err == nil || !body.closed {
				t.Fatalf("HTTP failure not handled: error=%v body closed=%v", err, body.closed)
			}
		})
	}
	t.Run("default timeout", func(t *testing.T) {
		stubUpdateTransport(t, func(request *http.Request) (*http.Response, error) {
			deadline, ok := request.Context().Deadline()
			if !ok || time.Until(deadline) > defaultUpdateRequestTimeout {
				t.Fatal("update check must always have a timeout")
			}
			return nil, context.DeadlineExceeded
		})
		if _, _, err := fetchLatestRelease("https://github.com/owner/repo", 0); err == nil || !strings.Contains(err.Error(), "timed out") {
			t.Fatalf("expected timeout error, got %v", err)
		}
	})
}

func TestOpenBrowserRejectsUnsafeURLsBeforeLaunching(t *testing.T) {
	for _, target := range []string{
		"file:///tmp/evil.exe", "javascript:alert(1)", "ms-settings:display", "--help",
		"http://github.com/owner/repo", "https://evil.example/owner/repo",
		"https://user:pass@github.com/owner/repo", "https://github.com/owner/repo?x=1",
		"https://github.com/owner/repo/releases/tag/../../evil", "https://github.com/login",
		"https://github.com/owner/repo/actions", "https://github.com/owner/repo/releases/tag/not-a-version",
	} {
		t.Run(target, func(t *testing.T) {
			if err := openBrowser(target); err == nil {
				t.Fatal("unsafe URL accepted")
			}
		})
	}
}
