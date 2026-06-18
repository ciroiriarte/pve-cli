package cli

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func lifecycleServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api2/json/cluster/resources", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[{"type":"qemu","vmid":100,"name":"web","node":"pve-01","status":"stopped"}]}`))
	})
	mux.HandleFunc("/api2/json/nodes/pve-01/qemu/100/clone", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		_ = r.ParseForm()
		if r.PostForm.Get("newid") != "200" {
			http.Error(w, "missing newid", http.StatusBadRequest)
			return
		}
		w.Write([]byte(`{"data":"UPID:pve-01:0001:0002:6A00:qmclone:100:root@pam!t:"}`))
	})
	mux.HandleFunc("/api2/json/nodes/pve-01/qemu/100", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		w.Write([]byte(`{"data":"UPID:pve-01:0003:0004:6A00:qmdestroy:100:root@pam!t:"}`))
	})
	return httptest.NewServer(mux)
}

func TestVMCloneReturnsTask(t *testing.T) {
	srv := lifecycleServer(t)
	defer srv.Close()
	out, err := runCLI(t, withCreds(srv, "vm", "clone", "100", "200", "--no-wait")...)
	if err != nil {
		t.Fatalf("vm clone: %v", err)
	}
	if !strings.Contains(out, "UPID:pve-01") || !strings.Contains(out, "qmclone") {
		t.Errorf("clone output missing task id:\n%s", out)
	}
}

func TestVMDeleteConfirmGate(t *testing.T) {
	srv := lifecycleServer(t)
	defer srv.Close()
	// Non-interactive without --yes must refuse.
	_, err := runCLI(t, withCreds(srv, "vm", "delete", "100")...)
	if err == nil || !strings.Contains(err.Error(), "refusing destructive action") {
		t.Fatalf("expected destructive refusal, got %v", err)
	}
}

func TestVMDeleteWithYes(t *testing.T) {
	srv := lifecycleServer(t)
	defer srv.Close()
	out, err := runCLI(t, withCreds(srv, "vm", "delete", "100", "--yes", "--no-wait")...)
	if err != nil {
		t.Fatalf("vm delete --yes: %v", err)
	}
	if !strings.Contains(out, "qmdestroy") {
		t.Errorf("delete output missing task id:\n%s", out)
	}
}
