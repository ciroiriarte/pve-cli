package cli

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// PBS < 3.3 has no sync-direction and rejects unknown parameters with a 400:
// job list retries the sync endpoint without it instead of failing.
func TestPBSJobListOldServerWithoutSyncDirection(t *testing.T) {
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api2/json/admin/sync" {
			http.Error(w, "no such path", http.StatusNotFound)
			return
		}
		queries = append(queries, r.URL.RawQuery)
		if r.URL.Query().Has("sync-direction") {
			http.Error(w, "parameter verification failed - 'sync-direction': schema does not allow additional properties.", http.StatusBadRequest)
			return
		}
		w.Write([]byte(`{"data":[{"id":"s-old","store":"store1","remote":"r1","remote-store":"rs"}]}`))
	}))
	t.Cleanup(srv.Close)

	var jobs []map[string]any
	runJSON(t, &jobs, withPBSCreds(&pbsFake{Server: srv}, "datastore", "job", "list", "--type", "sync")...)
	if len(jobs) != 1 || jobs[0]["id"] != "s-old" {
		t.Errorf("jobs = %v, want the single pull job", jobs)
	}
	if len(queries) != 2 || queries[1] != "" {
		t.Errorf("queries = %q, want a retry without sync-direction", queries)
	}
}

func TestRejectsParamOnlyMatchesNamed400(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "parameter verification failed - 'store': bad value", http.StatusBadRequest)
	}))
	t.Cleanup(srv.Close)
	_, err := runCLI(t, withPBSCreds(&pbsFake{Server: srv}, "datastore", "job", "list", "--type", "sync")...)
	if err == nil {
		t.Fatal("a 400 that does not name sync-direction must surface, not be retried away")
	}
}
