package transport

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/gorilla/websocket"

	"github.com/ciroiriarte/pve-cli/internal/auth"
)

func TestDialWebsocketAuthAndUpgrade(t *testing.T) {
	var sawAuth string
	up := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization")
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		// echo the first text frame back
		if _, msg, err := c.ReadMessage(); err == nil {
			_ = c.WriteMessage(websocket.TextMessage, msg)
		}
	}))
	defer srv.Close()

	tok, _ := auth.NewToken("root@pam!cli", "secret")
	cl, err := New(Options{BaseURL: srv.URL, Auth: tok})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := cl.DialWebsocket(context.Background(), "/nodes/n/qemu/100/vncwebsocket",
		url.Values{"port": {"5900"}, "vncticket": {"PVEVNC:abc"}})
	if err != nil {
		t.Fatalf("DialWebsocket: %v", err)
	}
	defer conn.Close()

	if sawAuth != "PVEAPIToken=root@pam!cli=secret" {
		t.Errorf("upgrade request missing token auth header; got %q", sawAuth)
	}
	if err := conn.WriteMessage(websocket.TextMessage, []byte("ping")); err != nil {
		t.Fatal(err)
	}
	if _, msg, err := conn.ReadMessage(); err != nil || string(msg) != "ping" {
		t.Errorf("echo failed: msg=%q err=%v", msg, err)
	}
}
