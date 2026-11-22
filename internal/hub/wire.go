package hub

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/romanpert/ainterlinked/internal/core"
)

// The hub process speaks newline-delimited JSON over a local socket: one
// request per line, answered by id, so a long call (wait) doesn't hold up
// the others on the same connection.
type wireReq struct {
	ID     int64           `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

type wireResp struct {
	ID     int64           `json:"id"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

// maxLine bounds one request or response (messages are at most 16 KB;
// lists of agents, tasks or a feed stay well under this).
const maxLine = 8 << 20

// Remote is a Client of the hub process.
type Remote struct {
	path string

	mu      sync.Mutex
	conn    net.Conn
	enc     *json.Encoder
	next    int64
	pending map[int64]chan wireResp
	// Who this client is on the hub: replayed after a reconnect (the hub
	// restarted, e.g. for a new binary) so callers never notice.
	replay    []wireReq
	replaying chan struct{} // closed when a reconnect's replay is done
	closed    bool
}

// Dial connects to the hub process listening at path.
func Dial(path string) (*Remote, error) {
	r := &Remote{path: path, pending: map[int64]chan wireResp{}}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.connect(); err != nil {
		return nil, err
	}
	return r, nil
}

// connect dials and starts the reader; r.mu is held.
func (r *Remote) connect() error {
	c, err := net.DialTimeout("unix", r.path, 2*time.Second)
	if err != nil {
		return err
	}
	r.conn, r.enc = c, json.NewEncoder(c)
	go r.read(c)
	return nil
}

func (r *Remote) read(c net.Conn) {
	sc := bufio.NewScanner(c)
	sc.Buffer(make([]byte, 64<<10), maxLine)
	for sc.Scan() {
		var resp wireResp
		if json.Unmarshal(sc.Bytes(), &resp) != nil {
			continue
		}
		r.mu.Lock()
		ch := r.pending[resp.ID]
		delete(r.pending, resp.ID)
		r.mu.Unlock()
		if ch != nil {
			ch <- resp
		}
	}
	// The connection is gone: fail what was in flight; the next call
	// reconnects.
	r.mu.Lock()
	if r.conn == c {
		r.conn = nil
	}
	for id, ch := range r.pending {
		ch <- wireResp{ID: id, Error: errLost.Error()}
		delete(r.pending, id)
	}
	r.mu.Unlock()
}

var errLost = errors.New("ainterlinked: lost the connection to the hub process")

// Call implements Client.
func (r *Remote) Call(ctx context.Context, method string, params, out any) error {
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	resp, err := r.roundTrip(ctx, method, raw)
	if err != nil {
		return err
	}
	if resp.Error != "" {
		return errors.New(resp.Error)
	}
	if method == "register" || method == "human" {
		r.remember(wireReq{Method: method, Params: raw})
	}
	if out == nil || len(resp.Result) == 0 {
		return nil
	}
	return json.Unmarshal(resp.Result, out)
}

func (r *Remote) roundTrip(ctx context.Context, method string, raw json.RawMessage) (wireResp, error) {
	for attempt := 0; ; attempt++ {
		ch, id, err := r.send(method, raw)
		if err != nil {
			return wireResp{}, err
		}
		select {
		case resp := <-ch:
			if resp.Error == errLost.Error() && attempt == 0 && (reads[method] || method == "register" || method == "human") {
				continue // the hub restarted: a read is safe to ask again
			}
			return resp, nil
		case <-ctx.Done():
			r.mu.Lock()
			delete(r.pending, id)
			r.mu.Unlock()
			return wireResp{}, ctx.Err()
		}
	}
}

// send writes one request, reconnecting (and replaying who this client
// is) if the connection was lost.
func (r *Remote) send(method string, raw json.RawMessage) (chan wireResp, int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for r.replaying != nil { // a reconnect is replaying: wait until it's done
		gate := r.replaying
		r.mu.Unlock()
		<-gate
		r.mu.Lock()
	}
	if r.closed {
		return nil, 0, errors.New("ainterlinked: hub client closed")
	}
	if r.conn == nil {
		if err := r.connect(); err != nil {
			return nil, 0, fmt.Errorf("ainterlinked: can't reach the hub process: %w", err)
		}
		// The hub answers requests concurrently, so who this client is must
		// be settled before anything else goes out on the new connection.
		var answers []chan wireResp
		for _, q := range r.replay {
			r.next++
			q.ID = r.next
			ch := make(chan wireResp, 1)
			r.pending[q.ID] = ch
			if err := r.enc.Encode(q); err != nil {
				return nil, 0, err
			}
			answers = append(answers, ch)
		}
		gate := make(chan struct{})
		r.replaying = gate
		r.mu.Unlock()
		for _, ch := range answers {
			select {
			case <-ch:
			case <-time.After(5 * time.Second):
			}
		}
		r.mu.Lock()
		r.replaying = nil
		close(gate)
		if r.conn == nil {
			return nil, 0, errLost
		}
	}
	r.next++
	ch := make(chan wireResp, 1)
	r.pending[r.next] = ch
	if err := r.enc.Encode(wireReq{ID: r.next, Method: method, Params: raw}); err != nil {
		delete(r.pending, r.next)
		r.conn.Close()
		r.conn = nil
		return nil, 0, err
	}
	return ch, r.next, nil
}

func (r *Remote) remember(q wireReq) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, old := range r.replay {
		if old.Method == q.Method && string(old.Params) == string(q.Params) {
			r.replay = append(r.replay[:i:i], r.replay[i+1:]...)
			break
		}
	}
	if len(r.replay) >= 8 {
		r.replay = r.replay[1:]
	}
	r.replay = append(r.replay, q)
}

// Human registers the operator through the hub process, which allows it
// only to a caller that isn't inside an agent harness.
func (r *Remote) Human(ctx context.Context, machine string) (core.Agent, error) {
	var a core.Agent
	err := r.Call(ctx, "human", map[string]string{"machine": machine}, &a)
	return a, err
}

// Close implements Client.
func (r *Remote) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	if r.conn != nil {
		return r.conn.Close()
	}
	return nil
}
