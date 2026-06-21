package cli

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// #17: `storage content upload` must stream a real multipart body (content field
// + file part) to the PVE upload endpoint.
func TestStorageUploadSendsMultipart(t *testing.T) {
	var gotContent, gotFileName, gotBody, gotCT string
	mux := http.NewServeMux()
	mux.HandleFunc("/api2/json/nodes/pve-01/storage/local/upload", func(w http.ResponseWriter, r *http.Request) {
		gotCT = r.Header.Get("Content-Type")
		mr, err := r.MultipartReader()
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			data, _ := io.ReadAll(part)
			switch part.FormName() {
			case "content":
				gotContent = string(data)
			case "filename":
				gotFileName = part.FileName()
				gotBody = string(data)
			}
		}
		w.Write([]byte(`{"data":"UPID:pve-01:0001:0002:6A00:imgcopy::root@pam:"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	iso := filepath.Join(t.TempDir(), "test.iso")
	if err := os.WriteFile(iso, []byte("ISO-DATA"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runCLI(t, withCreds(srv, "storage", "content", "upload", "local", iso, "--node", "pve-01", "--no-wait")...)
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	if !strings.HasPrefix(gotCT, "multipart/form-data") {
		t.Errorf("content-type = %q, want multipart/form-data", gotCT)
	}
	if gotContent != "iso" { // auto-detected from .iso
		t.Errorf("content field = %q, want iso", gotContent)
	}
	if gotFileName != "test.iso" {
		t.Errorf("filename = %q, want test.iso", gotFileName)
	}
	if gotBody != "ISO-DATA" {
		t.Errorf("uploaded body = %q, want ISO-DATA", gotBody)
	}
	if !strings.Contains(out, "imgcopy") {
		t.Errorf("expected the upload task UPID in output, got:\n%s", out)
	}
}

// Unknown extension without --content is a clear up-front error (no request made).
func TestStorageUploadRequiresContentTypeForUnknownExt(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("no request should be made when the content type can't be determined")
	}))
	defer srv.Close()

	blob := filepath.Join(t.TempDir(), "mystery.bin")
	_ = os.WriteFile(blob, []byte("x"), 0o644)

	_, err := runCLI(t, withCreds(srv, "storage", "content", "upload", "local", blob, "--node", "pve-01")...)
	if err == nil || !strings.Contains(err.Error(), "cannot detect content type") {
		t.Fatalf("expected content-type detection error, got %v", err)
	}
}
