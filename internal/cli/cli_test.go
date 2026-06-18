package cli

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

// runCLI executes the root command with args, capturing stdout. Credentials are
// passed as flags so no config file or env is needed.
func runCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	root := NewRootCmd()
	root.SetArgs(args)
	err := root.Execute()

	w.Close()
	os.Stdout = old
	out, _ := io.ReadAll(r)
	return string(out), err
}

func fakeServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api2/json/version", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		w.Write([]byte(`{"data":{"version":"8.2.2","release":"8.2","repoid":"abc"}}`))
	})
	mux.HandleFunc("/api2/json/nodes/pve-01/tasks/", func(w http.ResponseWriter, r *http.Request) {
		// .../tasks/<upid>/log
		if strings.HasSuffix(r.URL.Path, "/log") {
			w.Write([]byte(`{"data":[{"n":1,"t":"starting task"},{"n":2,"t":"TASK OK"}]}`))
			return
		}
		http.NotFound(w, r)
	})
	return httptest.NewServer(mux)
}

func withCreds(srv *httptest.Server, args ...string) []string {
	return append([]string{
		"--server", srv.URL,
		"--token-id", "u@pam!c",
		"--token-secret", "secret",
	}, args...)
}

func TestRawVersionExecutes(t *testing.T) {
	srv := fakeServer(t)
	defer srv.Close()

	out, err := runCLI(t, withCreds(srv, "raw", "version")...)
	if err != nil {
		t.Fatalf("pc raw version: %v", err)
	}
	if !strings.Contains(out, `"version": "8.2.2"`) {
		t.Errorf("output missing version data:\n%s", out)
	}
}

func TestRawListsRootsWithoutServer(t *testing.T) {
	out, err := runCLI(t, "raw")
	if err != nil {
		t.Fatalf("pc raw: %v", err)
	}
	for _, want := range []string{"nodes", "cluster", "version"} {
		if !strings.Contains(out, want) {
			t.Errorf("roots output missing %q:\n%s", want, out)
		}
	}
}

func TestTaskLogPrintsLines(t *testing.T) {
	srv := fakeServer(t)
	defer srv.Close()

	upid := "UPID:pve-01:0001ABCD:0F1E2D3C:65000000:qmstart:100:root@pam:"
	out, err := runCLI(t, withCreds(srv, "task", "log", upid)...)
	if err != nil {
		t.Fatalf("pc task log: %v", err)
	}
	if !strings.Contains(out, "starting task") || !strings.Contains(out, "TASK OK") {
		t.Errorf("log output missing expected lines:\n%s", out)
	}
}

func TestTaskLogFollowNoLossNoDup(t *testing.T) {
	// Stateful server: first status poll reports "running" with 2 log lines,
	// subsequent polls report "stopped" with a 3rd line appended. The log
	// endpoint honors the 0-based `start` param like the real PVE API.
	var statusPolls int32
	mux := http.NewServeMux()
	mux.HandleFunc("/api2/json/nodes/pve-01/tasks/", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/status"):
			n := atomic.AddInt32(&statusPolls, 1)
			if n == 1 {
				w.Write([]byte(`{"data":{"status":"running"}}`))
			} else {
				w.Write([]byte(`{"data":{"status":"stopped","exitstatus":"OK"}}`))
			}
		case strings.HasSuffix(r.URL.Path, "/log"):
			lines := []string{"line1", "line2"}
			if atomic.LoadInt32(&statusPolls) >= 1 {
				lines = append(lines, "line3") // appended once the task is stopping
			}
			start := 0
			if s := r.URL.Query().Get("start"); s != "" {
				start, _ = strconv.Atoi(s)
			}
			var sb strings.Builder
			sb.WriteString(`{"data":[`)
			for i := start; i < len(lines); i++ {
				if i > start {
					sb.WriteByte(',')
				}
				fmt.Fprintf(&sb, `{"n":%d,"t":%q}`, i+1, lines[i])
			}
			sb.WriteString(`]}`)
			w.Write([]byte(sb.String()))
		default:
			http.NotFound(w, r)
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	upid := "UPID:pve-01:0001ABCD:0F1E2D3C:65000000:qmstart:100:root@pam:"
	out, err := runCLI(t, withCreds(srv, "task", "log", upid, "--follow")...)
	if err != nil {
		t.Fatalf("pc task log --follow: %v", err)
	}
	// Each line exactly once, in order, including the trailing line3.
	if got := strings.Count(out, "line1"); got != 1 {
		t.Errorf("line1 count = %d, want 1 (dup/loss):\n%s", got, out)
	}
	if !strings.Contains(out, "line3") {
		t.Errorf("trailing line3 lost:\n%s", out)
	}
	lines := strings.Fields(out)
	want := []string{"line1", "line2", "line3"}
	if strings.Join(lines, " ") != strings.Join(want, " ") {
		t.Errorf("follow output = %v, want %v", lines, want)
	}
}

func TestRawUnknownSegmentErrors(t *testing.T) {
	_, err := runCLI(t, "raw", "nope")
	if err == nil {
		t.Fatal("expected error for unknown segment")
	}
	if !strings.Contains(err.Error(), "unknown path segment") {
		t.Errorf("err = %v", err)
	}
}
