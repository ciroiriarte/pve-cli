package transport

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// TestDeleteParamsGoInQuery guards the live-found bug where DELETE sent its
// params as a form body, which PVE rejects (HTTP 501 "Unexpected content").
func TestDeleteParamsGoInQuery(t *testing.T) {
	var gotMethod, gotQuery string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotQuery = r.URL.RawQuery
		gotBody, _ = io.ReadAll(r.Body)
		w.Write([]byte(`{"data":null}`))
	}))
	defer srv.Close()

	c, err := New(Options{BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	err = c.Do(context.Background(), &Request{
		Method: http.MethodDelete,
		Path:   "/nodes/n/qemu/100",
		Form:   url.Values{"purge": {"1"}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodDelete {
		t.Fatalf("method = %s", gotMethod)
	}
	if gotQuery != "purge=1" {
		t.Errorf("DELETE params must be in the query; got query %q", gotQuery)
	}
	if len(gotBody) != 0 {
		t.Errorf("DELETE must not send a body; got %q", gotBody)
	}
}

func TestPostParamsGoInBody(t *testing.T) {
	var gotBody []byte
	var gotCT string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		gotCT = r.Header.Get("Content-Type")
		w.Write([]byte(`{"data":null}`))
	}))
	defer srv.Close()

	c, _ := New(Options{BaseURL: srv.URL})
	if err := c.Do(context.Background(), &Request{
		Method: http.MethodPost,
		Path:   "/x",
		Form:   url.Values{"a": {"b"}},
	}, nil); err != nil {
		t.Fatal(err)
	}
	if string(gotBody) != "a=b" {
		t.Errorf("POST body = %q, want a=b", gotBody)
	}
	if gotCT != "application/x-www-form-urlencoded" {
		t.Errorf("content-type = %q", gotCT)
	}
}
