package transport

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

// DialWebsocket opens an authenticated websocket to an API path (e.g. a guest's
// vncwebsocket), reusing the client's TLS policy and auth (token header or
// ticket cookie). Used by the interactive console.
func (c *Client) DialWebsocket(ctx context.Context, path string, query url.Values) (*websocket.Conn, error) {
	u := *c.base
	u.Scheme = "wss"
	if c.base.Scheme == "http" {
		u.Scheme = "ws"
	}
	u.Path = "/api2/json" + path
	u.RawQuery = query.Encode()

	hdr := http.Header{}
	if c.auth != nil {
		// Capture auth on a throwaway request, then fold it into the upgrade
		// headers (Authorization for tokens; Cookie for tickets).
		r, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.base.String(), nil)
		if err := c.auth.Apply(r, false); err != nil {
			return nil, err
		}
		for k, v := range r.Header {
			hdr[k] = v
		}
		if cs := r.Cookies(); len(cs) > 0 {
			parts := make([]string, 0, len(cs))
			for _, ck := range cs {
				parts = append(parts, ck.Name+"="+ck.Value)
			}
			hdr.Set("Cookie", strings.Join(parts, "; "))
		}
	}

	// Proxmox's vncwebsocket negotiates the "binary" subprotocol.
	d := websocket.Dialer{TLSClientConfig: c.tlsConf, HandshakeTimeout: 15 * time.Second, Subprotocols: []string{"binary"}}
	conn, resp, err := d.DialContext(ctx, u.String(), hdr)
	if err != nil {
		if resp != nil {
			return nil, fmt.Errorf("websocket dial %s: %w (HTTP %d)", u.Redacted(), err, resp.StatusCode)
		}
		return nil, fmt.Errorf("websocket dial %s: %w", u.Redacted(), err)
	}
	return conn, nil
}
