package transcript

import (
	"bufio"
	"bytes"
	"encoding/json"
	"github.com/romanpert/ainterlinked/internal/env"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Tokens are what a model processed for a session.
type Tokens struct {
	In         int64 `json:"in,omitempty"`          // fresh input
	CacheWrite int64 `json:"cache_write,omitempty"` // input written to the prompt cache
	CacheRead  int64 `json:"cache_read,omitempty"`  // input read back from it
	Out        int64 `json:"out,omitempty"`
}

// Billed is what a cap counts: everything but cache reads, which are
// billed at a fraction of fresh input and which a long session repeats on
// every call (counting them would make a cap measure session length).
func (t Tokens) Billed() int64 { return t.In + t.CacheWrite + t.Out }

// Add returns t+u.
func (t Tokens) Add(u Tokens) Tokens {
	return Tokens{t.In + u.In, t.CacheWrite + u.CacheWrite, t.CacheRead + u.CacheRead, t.Out + u.Out}
}

// Causes of a turn, as keys of a Ledger.
const (
	CauseOwn  = ""      // a turn the user started (or the session's own)
	CausePeer = "~peer" // a turn another session started outside ainterlinked
)

// Ledger is a session's spend by cause: CauseOwn, CausePeer, or the ids of
// the ainterlinked messages that held the turn open ("#12,#15").
type Ledger map[string]Tokens

// Spender reads sessions' transcripts where it left off: the first read of
// a session goes through its whole transcript, later ones only through
// what the harness has written since.
type Spender struct {
	mu       sync.Mutex
	sessions map[string]*sessionLog
}

// NewSpender returns an empty Spender.
func NewSpender() *Spender { return &Spender{sessions: map[string]*sessionLog{}} }

// Read returns the session's spend so far. known is false when ainterlinked
// can't read this harness's transcripts, or the session's isn't found.
func (sp *Spender) Read(harness, session string) (Ledger, bool) {
	switch harness {
	case "claude-code":
		sp.mu.Lock()
		defer sp.mu.Unlock()
		// The file is pinned once found: another file of the same name
		// appearing later (newer, emptier) doesn't replace it.
		s := sp.sessions["claude-code|"+session]
		if s == nil || !regular(s.path) {
			main := claudeFind("", session)
			if main == "" {
				return nil, false
			}
			s = &sessionLog{path: main, subs: map[string]*cursor{}, by: Ledger{}}
			sp.sessions["claude-code|"+session] = s
		}
		s.claude(s.path)
		return s.ledger(), true
	case "codex":
		path := codexFind(session)
		if path == "" {
			return nil, false
		}
		t, ok := codexTotal(path)
		return Ledger{CauseOwn: t}, ok
	}
	return nil, false
}

type sessionLog struct {
	path     string // the session's transcript, as first found
	main     cursor
	subs     map[string]*cursor // subagents' transcripts, by file
	timeline []change           // the main transcript's turn starts, in order
	by       Ledger
}

type change struct {
	at    int64
	cause string
}

func (s *sessionLog) ledger() Ledger {
	out := Ledger{}
	for k, v := range s.by {
		out[k] = v
	}
	return out
}

// claude reads what Claude Code wrote since the last read: the session's
// transcript, then its subagents' (whose spend belongs to the turn that
// was running when they ran).
func (s *sessionLog) claude(main string) {
	s.main.read(main, func(line []byte) {
		if c, at, ok := claudeTurn(line); ok {
			s.main.cause = c
			s.timeline = append(s.timeline, change{at, c})
			return
		}
		s.count(&s.main, line, s.main.cause)
	})
	subs, _ := filepath.Glob(filepath.Join(strings.TrimSuffix(main, ".jsonl"), "subagents", "*.jsonl"))
	for _, f := range subs {
		c := s.subs[f]
		if c == nil {
			c = &cursor{}
			s.subs[f] = c
		}
		c.read(f, func(line []byte) {
			s.count(c, line, s.causeAt(lineTime(line)))
		})
	}
}

// causeAt is the cause of the main transcript's turn running at ms.
func (s *sessionLog) causeAt(ms int64) string {
	i := sort.Search(len(s.timeline), func(i int) bool { return s.timeline[i].at > ms })
	if i == 0 {
		return CauseOwn
	}
	return s.timeline[i-1].cause
}

// count adds an assistant entry's usage. Claude Code writes one entry per
// content block of a reply, each with the reply's usage so far: a reply is
// counted once, at the largest figures seen for its id.
func (s *sessionLog) count(c *cursor, line []byte, cause string) {
	if !bytes.Contains(line, []byte(`"usage"`)) {
		return
	}
	var e struct {
		Type    string `json:"type"`
		Message struct {
			ID    string `json:"id"`
			Usage struct {
				In         int64 `json:"input_tokens"`
				CacheWrite int64 `json:"cache_creation_input_tokens"`
				CacheRead  int64 `json:"cache_read_input_tokens"`
				Out        int64 `json:"output_tokens"`
			} `json:"usage"`
		} `json:"message"`
	}
	if json.Unmarshal(line, &e) != nil || e.Type != "assistant" || e.Message.ID == "" {
		return
	}
	u := e.Message.Usage
	now := Tokens{u.In, u.CacheWrite, u.CacheRead, u.Out}
	prev := c.seen(e.Message.ID)
	grown := Tokens{max(now.In, prev.In), max(now.CacheWrite, prev.CacheWrite), max(now.CacheRead, prev.CacheRead), max(now.Out, prev.Out)}
	delta := Tokens{grown.In - prev.In, grown.CacheWrite - prev.CacheWrite, grown.CacheRead - prev.CacheRead, grown.Out - prev.Out}
	c.remember(e.Message.ID, grown)
	s.by[cause] = s.by[cause].Add(delta)
}

// header is a delivered message's opening line. Its last "| fence X>>" is
// the hub's: the line ends there, and a subject can't break the line.
var header = regexp.MustCompile(`<<peer-message #(\d+) from [^\n]*\| fence ([0-9a-f]+)>>`)

// heldFor returns the ids of the ainterlinked messages a Stop hook held the
// turn open for: each fenced message's header, skipping its body up to its
// closing fence, so text inside a body (the sender's) can't name others.
// One delivery has one fence nonce, which the hub strips from bodies and
// subjects. Only the hub's own preamble comes before the first header, so
// that one is genuine. Each later header must start the line right after
// the previous closing fence and carry the same nonce. Anything else at
// that point (the "+K more" note, the group briefs, whose lines the
// senders write) ends the reading. A body can't end it early: the reading
// never searches inside a body for anything but its own closing fence.
// A delivery of briefs alone names no message.
func heldFor(text string) []string {
	m := header.FindStringSubmatchIndex(text)
	if m == nil {
		return nil
	}
	if b := strings.Index(text[:m[0]], "<<peer-briefs:"); b >= 0 {
		return nil
	}
	nonce := text[m[4]:m[5]]
	var ids []string
	for {
		id := text[m[2]:m[3]]
		rest := text[m[1]:]
		end := strings.Index(rest, "<<end #"+id+" | fence "+nonce)
		if end < 0 {
			return ids
		}
		ids = append(ids, "#"+id)
		text = rest[end:]
		nl := strings.IndexByte(text, '\n')
		if nl < 0 {
			return ids
		}
		text = text[nl+1:]
		line := text
		if nl := strings.IndexByte(line, '\n'); nl >= 0 {
			line = line[:nl]
		}
		if m = header.FindStringSubmatchIndex(line); m == nil || m[0] != 0 || line[m[4]:m[5]] != nonce {
			return ids
		}
	}
}

// regular reports whether path is (still) a regular file.
func regular(path string) bool {
	fi, err := os.Lstat(path)
	return err == nil && fi.Mode().IsRegular()
}

// claudeTurn reports an entry that starts a turn, and its cause: a prompt
// (the user's, typed or queued while the agent worked), another session's
// message, or ainterlinked holding the turn open at its end for peer mail.
// What the session itself set going carries on the turn that set it going:
// background task notifications, and subagents' hand-backs (which Claude
// Code marks as coming from a peer).
func claudeTurn(line []byte) (cause string, at int64, ok bool) {
	hold := bytes.Contains(line, []byte(`Stop hook feedback`))
	if !hold && !bytes.Contains(line, []byte(`"origin"`)) && !bytes.Contains(line, []byte(`"turnOrigin"`)) {
		return "", 0, false
	}
	var e struct {
		Type       string `json:"type"`
		IsMeta     bool   `json:"isMeta"`
		Timestamp  string `json:"timestamp"`
		TurnOrigin string `json:"turnOrigin"`
		Origin     *struct {
			Kind string `json:"kind"`
		} `json:"origin"`
		Message struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal(line, &e) != nil || (e.Type != "user" && e.Type != "attachment") {
		return "", 0, false
	}
	t, err := time.Parse(time.RFC3339Nano, e.Timestamp)
	if err != nil {
		return "", 0, false
	}
	if hold && e.IsMeta {
		var text string
		if json.Unmarshal(e.Message.Content, &text) == nil && strings.HasPrefix(text, "Stop hook feedback:") &&
			(strings.Contains(text, "[ainterlinked]") || strings.Contains(text, "[interlinked]")) {
			ids := heldFor(text)
			sort.Strings(ids)
			return strings.Join(ids, ","), t.UnixMilli(), len(ids) > 0
		}
	}
	how := e.TurnOrigin
	if e.Origin != nil {
		how = e.Origin.Kind
	}
	switch how {
	case "human", "sdk":
		// A session ainterlinked woke, whatever Claude Code calls that entry.
		if strings.Contains(textOf(e.Message.Content), WakeMark) {
			return CausePeer, t.UnixMilli(), true
		}
		return CauseOwn, t.UnixMilli(), true
	case "peer":
		var text string
		if json.Unmarshal(e.Message.Content, &text) == nil && handBack(text) {
			return "", 0, false
		}
		return CausePeer, t.UnixMilli(), true
	}
	return "", 0, false
}

// handBack says whether a peer entry is one of the session's own subagents
// handing its work back, as Claude Code writes it:
//
//	Another Claude session sent a message:
//	<agent-message from="a6185275a1d7abc94">
//	[Subagent hand-back] ...
//
// Only the start is read. Another session's message comes wrapped in
// <cross-session-message ...>, so whatever its sender writes can't take
// this shape.
func handBack(text string) bool {
	rest, ok := strings.CutPrefix(text, "Another Claude session sent a message:")
	if !ok {
		return false
	}
	rest, ok = strings.CutPrefix(strings.TrimSpace(rest), `<agent-message from="`)
	if !ok {
		return false
	}
	end := strings.Index(rest, `">`)
	if end < 0 || strings.ContainsAny(rest[:end], "\"<>\n") {
		return false
	}
	return strings.HasPrefix(strings.TrimSpace(rest[end+2:]), "[Subagent hand-back]")
}

func lineTime(line []byte) int64 {
	var e struct {
		Timestamp string `json:"timestamp"`
	}
	if json.Unmarshal(line, &e) != nil {
		return 0
	}
	t, err := time.Parse(time.RFC3339Nano, e.Timestamp)
	if err != nil {
		return 0
	}
	return t.UnixMilli()
}

// cursor is where a transcript was last read up to.
type cursor struct {
	offset int64
	cause  string
	ids    []string // recent replies, for streaming duplicates
	usage  map[string]Tokens
}

const recentReplies = 64

func (c *cursor) seen(id string) Tokens { return c.usage[id] }

func (c *cursor) remember(id string, t Tokens) {
	if c.usage == nil {
		c.usage = map[string]Tokens{}
	}
	if _, ok := c.usage[id]; !ok {
		c.ids = append(c.ids, id)
		if len(c.ids) > recentReplies {
			delete(c.usage, c.ids[0])
			c.ids = c.ids[1:]
		}
	}
	c.usage[id] = t
}

// read calls visit on each complete line written since the last read. A
// line the harness is still writing is left for the next read.
func (c *cursor) read(path string, visit func([]byte)) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil || fi.Size() <= c.offset {
		return
	}
	if _, err := f.Seek(c.offset, 0); err != nil {
		return
	}
	r := bufio.NewReaderSize(f, 256<<10)
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			return // EOF: an unfinished line waits for the next read
		}
		c.offset += int64(len(line))
		if l := bytes.TrimSpace(line); len(l) > 0 {
			visit(l)
		}
	}
}

// codexFind returns Codex's transcript of a session:
// ~/.codex/sessions/YYYY/MM/DD/rollout-<time>-<session id>.jsonl.
func codexFind(session string) string {
	h, err := env.OSHome()
	if err != nil || !safeID(session) {
		return ""
	}
	root, err := filepath.EvalSymlinks(filepath.Join(h, ".codex", "sessions"))
	if err != nil {
		return ""
	}
	matches, _ := filepath.Glob(filepath.Join(root, "*", "*", "*", "rollout-*-"+session+".jsonl"))
	for _, m := range matches {
		real, err := filepath.EvalSymlinks(m)
		if err != nil || !samePath(filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(real)))), root) {
			continue
		}
		if fi, err := os.Lstat(real); err == nil && fi.Mode().IsRegular() {
			return real
		}
	}
	return ""
}

// codexTotal is the last running total Codex wrote (token_count events
// carry the session's totals so far). A later, smaller total doesn't lower
// what the hub recorded (core keeps the larger).
func codexTotal(path string) (Tokens, bool) {
	f, err := os.Open(path)
	if err != nil {
		return Tokens{}, false
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return Tokens{}, false
	}
	var out Tokens
	found := false
	_ = scanBack(f, fi.Size(), Budget, func(line []byte) bool {
		if !bytes.Contains(line, []byte(`"total_token_usage"`)) {
			return true
		}
		var e struct {
			Payload struct {
				Type string `json:"type"`
				Info struct {
					Total struct {
						In        int64 `json:"input_tokens"`
						Cached    int64 `json:"cached_input_tokens"`
						CacheMade int64 `json:"cache_write_input_tokens"`
						Out       int64 `json:"output_tokens"`
					} `json:"total_token_usage"`
				} `json:"info"`
			} `json:"payload"`
		}
		if json.Unmarshal(line, &e) != nil || e.Payload.Type != "token_count" {
			return true
		}
		t := e.Payload.Info.Total
		// Codex's input count includes the cached part.
		out = Tokens{In: max(t.In-t.Cached-t.CacheMade, 0), CacheWrite: t.CacheMade, CacheRead: t.Cached, Out: t.Out}
		found = true
		return false
	})
	return out, found
}
