package monitor

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

// Home Assistant WebSocket API client shared by the haregistry and haconfig monitors:
// dial, authenticate, then send batches of commands pipelined (all written first,
// answers read in any order).

type haWS struct {
	c    *websocket.Conn
	next int
}

// haCmd is one command; args are merged into the message. Optional commands (opt)
// may fail (e.g. need an admin token): their error is kept in err instead.
type haCmd struct {
	typ  string
	args map[string]any
	into any
	opt  bool
	err  string
}

// haDial connects and authenticates. close must be called when done.
func haDial(ctx context.Context, t haTarget) (*haWS, func(), error) {
	u := t.base + "/api/websocket"
	switch {
	case strings.HasPrefix(u, "https://"):
		u = "wss://" + strings.TrimPrefix(u, "https://")
	case strings.HasPrefix(u, "http://"):
		u = "ws://" + strings.TrimPrefix(u, "http://")
	}
	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
		TLSClientConfig:  &tls.Config{InsecureSkipVerify: t.insecure},
		Proxy:            http.ProxyFromEnvironment,
	}
	c, _, err := dialer.DialContext(ctx, u, nil)
	if err != nil {
		return nil, nil, err
	}
	c.SetReadLimit(128 << 20)
	if dl, ok := ctx.Deadline(); ok {
		c.SetReadDeadline(dl)
		c.SetWriteDeadline(dl)
	}
	stop := context.AfterFunc(ctx, func() { c.Close() })
	closer := func() { stop(); c.Close() }
	var msg struct {
		Type string `json:"type"`
	}
	if err := c.ReadJSON(&msg); err != nil || msg.Type != "auth_required" {
		closer()
		return nil, nil, fmt.Errorf("no WebSocket handshake (%v)", err)
	}
	if err := c.WriteJSON(map[string]string{"type": "auth", "access_token": t.token}); err != nil {
		closer()
		return nil, nil, err
	}
	if err := c.ReadJSON(&msg); err != nil {
		closer()
		return nil, nil, err
	}
	if msg.Type != "auth_ok" {
		closer()
		return nil, nil, fmt.Errorf("refused the access token")
	}
	return &haWS{c: c}, closer, nil
}

// run sends all commands, then reads their answers.
func (w *haWS) run(cmds []*haCmd) error {
	first := w.next + 1
	for _, cmd := range cmds {
		w.next++
		m := map[string]any{}
		for k, v := range cmd.args {
			m[k] = v
		}
		m["id"], m["type"] = w.next, cmd.typ
		if err := w.c.WriteJSON(m); err != nil {
			return err
		}
	}
	var msg struct {
		ID      int             `json:"id"`
		Type    string          `json:"type"`
		Success bool            `json:"success"`
		Result  json.RawMessage `json:"result"`
		Error   struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	for done := 0; done < len(cmds); {
		msg.Result, msg.Success, msg.Error.Code, msg.Error.Message = nil, false, "", ""
		if err := w.c.ReadJSON(&msg); err != nil {
			return err
		}
		if msg.Type != "result" || msg.ID < first || msg.ID >= first+len(cmds) {
			continue
		}
		done++
		cmd := cmds[msg.ID-first]
		if !msg.Success {
			e := strings.TrimSpace(msg.Error.Code + " " + msg.Error.Message)
			if cmd.opt {
				cmd.err = e
				continue
			}
			return fmt.Errorf("%s: %s", cmd.typ, e)
		}
		if err := json.Unmarshal(msg.Result, cmd.into); err != nil {
			return fmt.Errorf("%s: %v", cmd.typ, err)
		}
	}
	return nil
}
