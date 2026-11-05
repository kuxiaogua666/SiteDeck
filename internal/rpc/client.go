package rpc

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"time"

	"sitedeck/internal/identity"
	"sitedeck/internal/protocol"
)

type Client struct {
	conn     net.Conn
	mu       sync.Mutex
	writeMu  sync.Mutex
	pending  map[string]chan protocol.Response
	done     chan struct{}
	err      error
	clientID string
}

func NewClient(conn net.Conn) *Client {
	c := &Client{conn: conn, pending: map[string]chan protocol.Response{}, done: make(chan struct{}), clientID: identity.Random(16)}
	go c.read()
	return c
}
func (c *Client) read() {
	scanner := bufio.NewScanner(c.conn)
	scanner.Buffer(make([]byte, 65536), 16<<20)
	for scanner.Scan() {
		var r protocol.Response
		if err := json.Unmarshal(scanner.Bytes(), &r); err != nil {
			c.finish(err)
			return
		}
		if r.Version != 1 {
			c.finish(fmt.Errorf("unsupported RPC protocol version"))
			return
		}
		c.mu.Lock()
		ch := c.pending[r.ID]
		delete(c.pending, r.ID)
		c.mu.Unlock()
		if ch != nil {
			ch <- r
		}
	}
	err := scanner.Err()
	if err == nil {
		err = fmt.Errorf("connection closed; unacknowledged operation outcome is unknown")
	}
	c.finish(err)
}
func (c *Client) finish(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	select {
	case <-c.done:
		return
	default:
	}
	c.err = err
	close(c.done)
	_ = c.conn.Close()
}
func (c *Client) Close() error          { c.finish(fmt.Errorf("client closed")); return nil }
func (c *Client) Done() <-chan struct{} { return c.done }
func (c *Client) send(req protocol.Request) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_ = c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	err := json.NewEncoder(c.conn).Encode(req)
	_ = c.conn.SetWriteDeadline(time.Time{})
	return err
}
func (c *Client) Call(ctx context.Context, method string, params any) (json.RawMessage, *protocol.Error) {
	return c.CallAs(ctx, method, params, c.clientID)
}
func (c *Client) CallAs(ctx context.Context, method string, params any, clientID string) (json.RawMessage, *protocol.Error) {
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, protocol.Fail("INVALID_PARAMS", err.Error())
	}
	if len(raw) > 96<<10 {
		return nil, protocol.Fail("INVALID_PARAMS", "request parameters exceed 96 KiB")
	}
	id := identity.Random(12)
	timeout := 30000
	if d, ok := ctx.Deadline(); ok {
		timeout = int(time.Until(d).Milliseconds())
		if timeout < 1 {
			timeout = 1
		}
	}
	ch := make(chan protocol.Response, 1)
	c.mu.Lock()
	select {
	case <-c.done:
		err := c.err
		c.mu.Unlock()
		return nil, protocol.Fail("TRANSPORT_CLOSED", err.Error())
	default:
	}
	c.pending[id] = ch
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(c.pending, id); c.mu.Unlock() }()
	if err = c.send(protocol.Request{Version: 1, ID: id, Method: method, Params: raw, TimeoutMS: timeout, ClientID: clientID}); err != nil {
		return nil, protocol.Fail("TRANSPORT_CLOSED", err.Error())
	}
	select {
	case r := <-ch:
		if r.Error != nil {
			return nil, r.Error
		}
		b, err := json.Marshal(r.Result)
		if err != nil {
			return nil, protocol.Fail("INVALID_RESPONSE", err.Error())
		}
		return b, nil
	case <-ctx.Done():
		b, _ := json.Marshal(map[string]string{"id": id})
		_ = c.send(protocol.Request{Version: 1, ID: identity.Random(12), Method: "request.cancel", Params: b, ClientID: clientID})
		return nil, protocol.Fail("CANCELED", "request canceled; verify the remote outcome before retrying a write")
	case <-c.done:
		c.mu.Lock()
		err := c.err
		c.mu.Unlock()
		return nil, protocol.Fail("TRANSPORT_CLOSED", err.Error())
	}
}
