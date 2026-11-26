package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// correctnessServer serves a VM 100 and CT 101 on pve-01 plus the endpoints
// exercised by the #29/#36 tests. hits counts requests per "METHOD path".
func correctnessServer(t *testing.T, hits map[string]int) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api2/json/cluster/resources", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[
			{"type":"node","node":"pve-01","status":"online","maxcpu":8,"cpu":0.25},
			{"type":"qemu","vmid":100,"name":"web","node":"pve-01","status":"running"},
			{"type":"lxc","vmid":101,"name":"ct1","node":"pve-01","status":"running"}
		]}`))
	})
	task := func(typ string) string {
		return `{"data":"UPID:pve-01:0001:0002:6A00:` + typ + `:100:root@pam!t:"}`
	}
	record := func(r *http.Request) { hits[r.Method+" "+r.URL.Path]++ }
	mux.HandleFunc("/api2/json/nodes/pve-01/qemu/100/agent/shutdown", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		w.Write([]byte(`{"data":null}`))
	})
	mux.HandleFunc("/api2/json/nodes/pve-01/qemu/100/agent/ping", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		w.Write([]byte(`{"data":null}`))
	})
	mux.HandleFunc("/api2/json/nodes/pve-01/qemu/100/move_disk", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		w.Write([]byte(task("qmmove")))
	})
	mux.HandleFunc("/api2/json/nodes/pve-01/lxc/101/move_volume", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		w.Write([]byte(task("move_volume")))
	})
	mux.HandleFunc("/api2/json/access/acl", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		w.Write([]byte(`{"data":null}`))
	})
	// task polling for the move commands' default --wait
	mux.HandleFunc("/api2/json/nodes/pve-01/tasks/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":{"status":"stopped","exitstatus":"OK"}}`))
	})
	return httptest.NewServer(mux)
}

// #29: the query must travel as a real query string, not a %3F-escaped path.
func TestCloudInitDumpSendsQuerySeparately(t *testing.T) {
	var gotPath, gotType, gotRawPath string
	mux := http.NewServeMux()
	mux.HandleFunc("/api2/json/cluster/resources", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[{"type":"qemu","vmid":100,"name":"web","node":"pve-01","status":"running"}]}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotRawPath, gotType = r.URL.Path, r.URL.RawPath, r.URL.Query().Get("type")
		w.Write([]byte(`{"data":"#cloud-config\n"}`))
	})
	s2 := httptest.NewServer(mux)
	defer s2.Close()

	if _, err := runCLI(t, withCreds(s2, "vm", "cloudinit", "100", "--dump", "user")...); err != nil {
		t.Fatalf("cloudinit --dump: %v", err)
	}
	if gotPath != "/api2/json/nodes/pve-01/qemu/100/cloudinit/dump" || gotRawPath != "" {
		t.Errorf("path = %q (raw %q), want .../cloudinit/dump with no escaped query", gotPath, gotRawPath)
	}
	if gotType != "user" {
		t.Errorf("query type = %q, want user", gotType)
	}
}

func TestPruneBackupsPreviewSendsQuerySeparately(t *testing.T) {
	var gotPath, gotVMID string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotVMID = r.URL.Path, r.URL.Query().Get("vmid")
		w.Write([]byte(`{"data":[{"volid":"local:backup/vzdump-qemu-100.vma","type":"qemu","vmid":100,"mark":"keep"}]}`))
	}))
	defer srv.Close()

	out, err := runCLI(t, withCreds(srv, "storage", "prune-backups", "local", "--node", "pve-01", "--vmid", "100")...)
	if err != nil {
		t.Fatalf("prune-backups preview: %v", err)
	}
	if gotPath != "/api2/json/nodes/pve-01/storage/local/prunebackups" {
		t.Errorf("path = %q", gotPath)
	}
	if gotVMID != "100" {
		t.Errorf("query vmid = %q, want 100", gotVMID)
	}
	if !strings.Contains(out, "keep") {
		t.Errorf("preview output missing rows:\n%s", out)
	}
}

// poolServer answers POST /pools with the given body, so rawMutate's response
// handling (#32) can be driven through a real rawMutate caller.
func poolServer(body string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(body))
	}))
}

func TestRawMutateResponseHandling(t *testing.T) {
	cases := []struct {
		name, body string
		wantErr    string // "" = success
		wantOut    string
	}{
		{name: "null is sync success", body: `{"data":null}`},
		{name: "empty string is sync success", body: `{"data":""}`},
		{name: "malformed envelope errors", body: `{"data":`, wantErr: "unexpected response"},
		{name: "unparsable UPID errors", body: `{"data":"UPID:::::::"}`, wantErr: "malformed task id"},
		{name: "object is rendered", body: `{"data":{"answer":42}}`, wantOut: "answer"},
		{name: "plain string is rendered", body: `{"data":"ok-123"}`, wantOut: "ok-123"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := poolServer(tc.body)
			defer srv.Close()
			out, err := runCLI(t, withCreds(srv, "pool", "create", "p1")...)
			if tc.wantErr == "" && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
			}
			if !strings.Contains(out, tc.wantOut) {
				t.Errorf("output missing %q:\n%s", tc.wantOut, out)
			}
		})
	}
}

// #31: token create must print the returned object, including the one-time
// secret `value`, and emit it as a native object for json.
func TestAccessTokenCreatePrintsSecret(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api2/json/access/users/ops@pdm/token/ci" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"data":{"full-tokenid":"ops@pdm!ci","info":{},"value":"s3cr3t-uuid"}}`))
	}))
	defer srv.Close()

	out, err := runCLI(t, withPDMCreds(srv, "access", "token", "create", "ops@pdm", "ci")...)
	if err != nil {
		t.Fatalf("token create: %v", err)
	}
	if !strings.Contains(out, "s3cr3t-uuid") {
		t.Errorf("table output missing the secret value:\n%s", out)
	}

	out, err = runCLI(t, withPDMCreds(srv, "access", "token", "create", "ops@pdm", "ci", "-o", "json")...)
	if err != nil {
		t.Fatalf("token create -o json: %v", err)
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(out), &obj); err != nil {
		t.Fatalf("json output is not an object: %v\n%s", err, out)
	}
	if obj["value"] != "s3cr3t-uuid" || obj["full-tokenid"] != "ops@pdm!ci" {
		t.Errorf("json object = %v", obj)
	}
}

// #35: singular reads emit an object for json/yaml, not a one-element array.
func TestNodeShowJSONYAMLIsObject(t *testing.T) {
	srv := correctnessServer(t, map[string]int{})
	defer srv.Close()

	out, err := runCLI(t, withCreds(srv, "node", "show", "pve-01", "-o", "json")...)
	if err != nil {
		t.Fatalf("node show -o json: %v", err)
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(out), &obj); err != nil {
		t.Fatalf("node show json is not an object: %v\n%s", err, out)
	}
	if obj["node"] != "pve-01" || obj["status"] != "online" {
		t.Errorf("node show json = %v", obj)
	}

	out, err = runCLI(t, withCreds(srv, "node", "show", "pve-01", "-o", "yaml")...)
	if err != nil {
		t.Fatalf("node show -o yaml: %v", err)
	}
	obj = nil
	if err := yaml.Unmarshal([]byte(out), &obj); err != nil {
		t.Fatalf("node show yaml is not a mapping: %v\n%s", err, out)
	}
	// yaml keys follow the Go field names (no yaml tags on domain.Node).
	if obj["name"] != "pve-01" || obj["status"] != "online" {
		t.Errorf("node show yaml = %v", obj)
	}
}

func TestRemoteShowJSONYAMLIsObject(t *testing.T) {
	srv := pdmServer(t)
	defer srv.Close()

	out, err := runCLI(t, withPDMCreds(srv, "remote", "show", "dc-east", "-o", "json")...)
	if err != nil {
		t.Fatalf("remote show -o json: %v", err)
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(out), &obj); err != nil {
		t.Fatalf("remote show json is not an object: %v\n%s", err, out)
	}
	if obj["id"] != "dc-east" || obj["type"] != "pve" {
		t.Errorf("remote show json = %v", obj)
	}

	out, err = runCLI(t, withPDMCreds(srv, "remote", "show", "dc-east", "-o", "yaml")...)
	if err != nil {
		t.Fatalf("remote show -o yaml: %v", err)
	}
	obj = nil
	if err := yaml.Unmarshal([]byte(out), &obj); err != nil {
		t.Fatalf("remote show yaml is not a mapping: %v\n%s", err, out)
	}
	if obj["id"] != "dc-east" {
		t.Errorf("remote show yaml = %v", obj)
	}
}

// #36: disruptive/data-removing commands refuse non-interactively without
// --yes, and proceed with it.
func TestDisruptiveCommandsConfirmGate(t *testing.T) {
	cases := []struct {
		name string
		args []string
		hit  string
	}{
		{"agent shutdown", []string{"vm", "agent", "shutdown", "100"}, "POST /api2/json/nodes/pve-01/qemu/100/agent/shutdown"},
		{"move-disk --delete", []string{"vm", "move-disk", "100", "--disk", "scsi0", "--storage", "ceph", "--delete"}, "POST /api2/json/nodes/pve-01/qemu/100/move_disk"},
		{"ct move-volume --delete", []string{"ct", "move-volume", "101", "--volume", "rootfs", "--storage", "ceph", "--delete"}, "POST /api2/json/nodes/pve-01/lxc/101/move_volume"},
		{"acl set --delete", []string{"access", "acl", "set", "--path", "/vms/100", "--roles", "PVEAuditor", "--ids", "ops@pve", "--delete"}, "PUT /api2/json/access/acl"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hits := map[string]int{}
			srv := correctnessServer(t, hits)
			defer srv.Close()

			_, err := runCLI(t, withCreds(srv, tc.args...)...)
			if err == nil || !strings.Contains(err.Error(), "refusing destructive action") {
				t.Fatalf("expected confirm refusal, got %v", err)
			}
			if hits[tc.hit] != 0 {
				t.Fatalf("endpoint hit without confirmation")
			}

			if _, err := runCLI(t, withCreds(srv, append(tc.args, "--yes")...)...); err != nil {
				t.Fatalf("with --yes: %v", err)
			}
			if hits[tc.hit] != 1 {
				t.Fatalf("expected %s once with --yes, got %d", tc.hit, hits[tc.hit])
			}
		})
	}
}

// The gate applies only to the destructive variant: no --delete / ping need
// no confirmation.
func TestNonDestructiveVariantsNeedNoConfirm(t *testing.T) {
	cases := []struct {
		name string
		args []string
		hit  string
	}{
		{"agent ping", []string{"vm", "agent", "ping", "100"}, "POST /api2/json/nodes/pve-01/qemu/100/agent/ping"},
		{"move-disk", []string{"vm", "move-disk", "100", "--disk", "scsi0", "--storage", "ceph"}, "POST /api2/json/nodes/pve-01/qemu/100/move_disk"},
		{"ct move-volume", []string{"ct", "move-volume", "101", "--volume", "rootfs", "--storage", "ceph"}, "POST /api2/json/nodes/pve-01/lxc/101/move_volume"},
		{"acl set", []string{"access", "acl", "set", "--path", "/vms/100", "--roles", "PVEAuditor", "--ids", "ops@pve"}, "PUT /api2/json/access/acl"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hits := map[string]int{}
			srv := correctnessServer(t, hits)
			defer srv.Close()
			if _, err := runCLI(t, withCreds(srv, tc.args...)...); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			if hits[tc.hit] != 1 {
				t.Fatalf("expected %s once, got %d", tc.hit, hits[tc.hit])
			}
		})
	}
}
