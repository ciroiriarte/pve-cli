package cli

import (
	"bytes"
	"fmt"
	"io"
	"net/url"
	"os"

	"github.com/gorilla/websocket"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/ciroiriarte/pve-cli/internal/protocol"
	"github.com/ciroiriarte/pve-cli/internal/provider"
)

// wsConn is the subset of *websocket.Conn the console bridge needs (so it can
// be faked in tests).
type wsConn interface {
	ReadMessage() (int, []byte, error)
	WriteMessage(messageType int, data []byte) error
	Close() error
}

// consoleQuit is the key that detaches the console (Ctrl-]).
const consoleQuit = 0x1d

// frame encodes terminal input using Proxmox's termproxy wire format: data is
// sent as "0:<bytelen>:<bytes>" (output from the server arrives raw).
func frame(b []byte) []byte {
	return []byte(fmt.Sprintf("0:%d:%s", len(b), b))
}

// runConsole performs the termproxy auth handshake then bridges in<->conn<->out
// until EOF, the quit byte, or a websocket error. It is transport-agnostic for
// testability.
func runConsole(conn wsConn, user, ticket string, in io.Reader, out io.Writer, cols, rows int) error {
	if err := conn.WriteMessage(websocket.TextMessage, []byte(user+":"+ticket+"\n")); err != nil {
		return fmt.Errorf("console auth: %w", err)
	}
	_, msg, err := conn.ReadMessage()
	if err != nil {
		return fmt.Errorf("console auth: %w", err)
	}
	if string(bytes.TrimSpace(msg)) != "OK" {
		return fmt.Errorf("console authentication rejected: %q", msg)
	}
	if cols > 0 && rows > 0 {
		_ = conn.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf("2:%d:%d:", cols, rows)))
	}

	done := make(chan error, 2)
	go func() { // server output -> local out (raw)
		for {
			_, m, err := conn.ReadMessage()
			if err != nil {
				done <- err
				return
			}
			if _, err := out.Write(m); err != nil {
				done <- err
				return
			}
		}
	}()
	go func() { // local input -> framed -> server, until quit byte / EOF
		buf := make([]byte, 4096)
		for {
			n, err := in.Read(buf)
			if n > 0 {
				data := buf[:n]
				if i := bytes.IndexByte(data, consoleQuit); i >= 0 {
					if i > 0 {
						_ = conn.WriteMessage(websocket.TextMessage, frame(data[:i]))
					}
					done <- nil
					return
				}
				if err := conn.WriteMessage(websocket.TextMessage, frame(data)); err != nil {
					done <- err
					return
				}
			}
			if err != nil {
				done <- err
				return
			}
		}
	}()
	err = <-done
	if err == io.EOF {
		return nil
	}
	return err
}

func newGuestConsoleCmd(a *app, spec guestSpec) *cobra.Command {
	var node string
	var serial int
	cmd := &cobra.Command{
		Use:   "console <vmid>",
		Short: fmt.Sprintf("Attach to a %s serial console (Ctrl-] to quit)", spec.label),
		Long: "Opens an interactive serial console over a websocket (termproxy). The guest\n" +
			"must have a serial port configured. PVE provider only. Ctrl-] detaches.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := a.Provider()
			if err != nil {
				return err
			}
			if p.Name() != "pve" {
				return fmt.Errorf("console is only available with the pve provider (not %s)", p.Name())
			}
			if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
				return fmt.Errorf("console requires an interactive terminal (stdin/stdout must be a TTY)")
			}
			g, err := resolveGuest(cmd.Context(), p, spec, args[0], node)
			if err != nil {
				return err
			}

			// 1. acquire a termproxy ticket
			tp := url.Values{"serial": {fmt.Sprintf("serial%d", serial)}}
			body, err := p.Raw(cmd.Context(), "POST",
				fmt.Sprintf("/nodes/%s/%s/%d/termproxy", g.Node, kindEndpoint(spec), g.VMID), tp)
			if err != nil {
				return err
			}
			var t struct {
				Ticket string `json:"ticket"`
				Port   any    `json:"port"`
				User   string `json:"user"`
			}
			if err := protocol.DecodeData(body, &t); err != nil {
				return fmt.Errorf("termproxy: %w", err)
			}

			// 2. open the websocket to the guest's vncwebsocket
			cl, err := provider.NewClient(a.settings, a.debug)
			if err != nil {
				return err
			}
			q := url.Values{"port": {fmt.Sprintf("%v", t.Port)}, "vncticket": {t.Ticket}}
			conn, err := cl.DialWebsocket(cmd.Context(),
				fmt.Sprintf("/nodes/%s/%s/%d/vncwebsocket", g.Node, kindEndpoint(spec), g.VMID), q)
			if err != nil {
				return err
			}

			// 3. raw local terminal + bridge. Defer order matters (LIFO): register
			// Restore first, then Close, so Close runs first on exit — the output
			// goroutine stops before the terminal is restored (no stray frame).
			fmt.Fprintf(stderrWriter(), "connected to %s %d serial console — press Ctrl-] to quit\n", spec.label, g.VMID)
			fd := int(os.Stdin.Fd())
			oldState, err := term.MakeRaw(fd)
			if err != nil {
				conn.Close()
				return err
			}
			defer term.Restore(fd, oldState)
			defer conn.Close()
			cols, rows, _ := term.GetSize(int(os.Stdout.Fd()))
			return runConsole(conn, t.User, t.Ticket, os.Stdin, os.Stdout, cols, rows)
		},
	}
	cmd.Flags().StringVar(&node, "node", "", "node hosting the guest")
	cmd.Flags().IntVar(&serial, "serial", 0, "serial port index (serialN)")
	return cmd
}
