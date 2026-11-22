package transcript

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Fill is what takes up a session's context window, as its harness's
// transcript tells it. Totals are the API's own token counts; how a total
// splits into pieces follows the length of the text each piece put in.
type Fill struct {
	Session     string       `json:"session"`
	Title       string       `json:"title,omitempty"`
	Model       string       `json:"model,omitempty"`
	Start       int64        `json:"start"` // tokens in the first request
	Now         int64        `json:"now"`   // in the latest
	Peak        int64        `json:"peak"`  // in the largest
	Begin       []Piece      `json:"begin,omitempty"`
	Tools       []Piece      `json:"tools,omitempty"` // of Begin's tool definitions
	Files       []Piece      `json:"files,omitempty"` // of Begin's instructions
	Grown       []Piece      `json:"grown,omitempty"` // since, compactions included
	Compactions []Compaction `json:"compactions,omitempty"`
}

// Piece is one source of context and the tokens it takes.
type Piece struct {
	Name   string `json:"name"`
	Tokens int64  `json:"tokens"`
}

// Compaction is one time the harness summarized the window to make room.
type Compaction struct {
	Auto bool  `json:"auto,omitempty"`
	From int64 `json:"from"`         // tokens before
	To   int64 `json:"to,omitempty"` // in the first request after it (0: none yet)
}

// Pieces that aren't a tool's, a server's or a hook's own.
const (
	PieceTools        = "tool definitions"
	PieceLoaded       = "tools loaded on demand"
	PieceSystem       = "system prompt"
	PieceInstructions = "instructions"
	PieceAinterlinked = "ainterlinked"
	PieceUnrecorded   = "not recorded (system prompt, tools)"
)

// imageChars stands for an image, whose text length in a transcript says
// nothing about its tokens.
const imageChars = 4000

// charsPerToken is used only where a transcript doesn't record part of the
// first request, so its pieces can't be scaled to the total.
const charsPerToken = 3

// ReadFill reads a Claude Code transcript's context, request by request.
func ReadFill(path string) (Fill, error) {
	if _, err := os.Stat(path); err != nil {
		return Fill{}, err
	}
	f := &filler{
		fill:  Fill{Session: strings.TrimSuffix(filepath.Base(path), ".jsonl")},
		added: map[string]float64{}, own: map[string]float64{}, grown: map[string]float64{},
		calls: map[string]string{},
	}
	(&cursor{}).read(path, f.line)
	if f.fill.Start == 0 {
		return Fill{}, errors.New("no request in it yet")
	}
	f.fill.Grown = pieces(f.grown)
	return f.fill, nil
}

// filler walks a transcript. What enters the window between two requests is
// their difference: the agent's reply takes its own output tokens, and the
// rest is shared among what was added, by length.
type filler struct {
	fill  Fill
	added map[string]float64 // chars put in the window since the last request
	own   map[string]float64 // chars of the latest reply, by what they were
	grown map[string]float64 // tokens
	calls map[string]string  // tool call id → piece

	reply     string // id of the latest reply
	out       int64  // its output tokens
	thinking  int64  // of them, thinking
	system    int    // chars of the largest system prompt and tool definitions seen
	tools     int
	shaped    bool
	compacted bool // since the last request
}

func (f *filler) line(line []byte) {
	// The type is the only field decoded for most of a transcript's bytes:
	// what hooks printed, file snapshots, copies of requests.
	head := line
	if len(head) > 4096 {
		head = head[:4096]
	}
	attachment := bytes.Contains(head, []byte(`"attachment"`))
	if attachment && !bytes.Contains(line, []byte(`"rendered"`)) && !bytes.Contains(head, []byte(`"deferred_tools_record"`)) {
		return // it never reached the model
	}
	if !attachment && !bytes.Contains(line, []byte(`"message"`)) && !bytes.Contains(head, []byte(`Title"`)) &&
		!bytes.Contains(head, []byte(`"compact_boundary"`)) && !bytes.Contains(head, []byte(`"api-request-shape"`)) {
		return
	}
	var e entry
	if json.Unmarshal(line, &e) != nil || e.IsSidechain {
		return
	}
	switch {
	case e.Type == "custom-title":
		f.fill.Title = e.CustomTitle
	case e.Type == "ai-title":
		if f.fill.Title == "" {
			f.fill.Title = e.AITitle
		}
	case e.Type == "api-request-shape":
		f.shape(e)
	case e.Type == "system" && e.Subtype == "compact_boundary":
		f.fill.Compactions = append(f.fill.Compactions, Compaction{Auto: e.Compact.Trigger == "auto", From: e.Compact.PreTokens})
		f.compacted = true
	case e.Type == "attachment":
		f.attach(e)
	case e.Type == "user":
		f.user(e.Message.Content)
	case e.Type == "assistant":
		f.assistant(e)
	}
}

type entry struct {
	Type        string `json:"type"`
	Subtype     string `json:"subtype"`
	IsSidechain bool   `json:"isSidechain"`
	CustomTitle string `json:"customTitle"`
	AITitle     string `json:"aiTitle"`
	Message     struct {
		ID      string          `json:"id"`
		Model   string          `json:"model"`
		Content json.RawMessage `json:"content"`
		Usage   struct {
			In         int64 `json:"input_tokens"`
			CacheWrite int64 `json:"cache_creation_input_tokens"`
			CacheRead  int64 `json:"cache_read_input_tokens"`
			Out        int64 `json:"output_tokens"`
			Details    struct {
				Thinking int64 `json:"thinking_tokens"`
			} `json:"output_tokens_details"`
		} `json:"usage"`
	} `json:"message"`
	Attachment struct {
		Type    string          `json:"type"`
		Content json.RawMessage `json:"content"`
		Entries json.RawMessage `json:"entries"`
		Files   []struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		} `json:"files"`
	} `json:"attachment"`
	Rendered []struct {
		Content json.RawMessage `json:"content"`
	} `json:"rendered"`
	Compact struct {
		Trigger   string `json:"trigger"`
		PreTokens int64  `json:"preTokens"`
	} `json:"compactMetadata"`
	Shape struct {
		System json.RawMessage   `json:"system"`
		Tools  []json.RawMessage `json:"tools"`
	} `json:"shape"`
}

// shape: what a request carries besides its messages. Only what's larger
// than before was added (side requests carry fewer tools).
func (f *filler) shape(e entry) {
	if n := len(e.Shape.System); n > f.system {
		f.added[PieceSystem] += float64(n - f.system)
		f.system = n
	}
	total := 0
	byName := map[string]float64{}
	for _, raw := range e.Shape.Tools {
		var t struct {
			Name string `json:"name"`
		}
		_ = json.Unmarshal(raw, &t)
		byName[t.Name] += float64(len(raw))
		total += len(raw)
	}
	if total > f.tools {
		name := PieceTools
		if f.fill.Start > 0 {
			name = PieceLoaded
		}
		f.added[name] += float64(total - f.tools)
		f.tools = total
	}
	if f.fill.Start == 0 && len(byName) > 0 {
		f.shaped, f.fill.Tools = true, pieces(byName)
	}
}

// attach: what the harness put in the window itself. rendered is the text
// the model got.
func (f *filler) attach(e entry) {
	a := e.Attachment
	if a.Type == "deferred_tools_record" {
		if !f.shaped { // else the requests' shapes count them
			f.added[PieceLoaded] += float64(len(a.Entries))
		}
		return
	}
	n := 0.0
	for _, r := range e.Rendered {
		n += chars(r.Content)
	}
	if n == 0 {
		return
	}
	switch a.Type {
	case "hook_additional_context", "hook_success", "hook_blocking_error":
		f.hook(a.Content, n)
	case "instructions":
		if f.fill.Start == 0 && len(a.Files) > 0 {
			byPath := map[string]float64{}
			for _, x := range a.Files {
				byPath[x.Path] += float64(len(x.Content))
			}
			f.fill.Files = pieces(byPath)
		}
		f.added[PieceInstructions] += n
	default:
		f.added[attachmentPiece(a.Type)] += n
	}
}

// hook shares n chars among the hooks whose output one attachment holds.
func (f *filler) hook(content json.RawMessage, n float64) {
	var texts []string
	if json.Unmarshal(content, &texts) != nil {
		var one string
		_ = json.Unmarshal(content, &one)
		texts = []string{one}
	}
	mine, all := 0.0, 0.0
	for _, t := range texts {
		all += float64(len(t))
		if ours(t) {
			mine += float64(len(t))
		}
	}
	if all == 0 {
		f.added["hooks"] += n
		return
	}
	if mine > 0 {
		f.added[PieceAinterlinked] += n * mine / all
	}
	if mine < all {
		f.added["hooks"] += n * (all - mine) / all
	}
}

// ours says whether a hook's output is ainterlinked's (interlinked, before
// its rename).
func ours(t string) bool {
	return strings.Contains(t, "interlinked]") || strings.Contains(t, "<<peer-")
}

func attachmentPiece(kind string) string {
	switch kind {
	case "queued_command":
		return "messages and notices"
	case "skill_listing":
		return "skill list"
	case "agent_listing_delta":
		return "subagent list"
	case "deferred_tools_delta":
		return "names of tools on demand"
	case "mcp_instructions_delta":
		return "MCP servers' instructions"
	case "environment", "session_context", "model", "date", "auto_mode", "remote_session_change":
		return "environment"
	}
	return strings.ReplaceAll(kind, "_", " ")
}

func (f *filler) user(content json.RawMessage) {
	var text string
	if json.Unmarshal(content, &text) == nil {
		f.prompt(text)
		return
	}
	var blocks []struct {
		Type    string          `json:"type"`
		Text    string          `json:"text"`
		Call    string          `json:"tool_use_id"`
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(content, &blocks) != nil {
		return
	}
	for _, b := range blocks {
		switch b.Type {
		case "text":
			f.prompt(b.Text)
		case "image", "document":
			f.added["prompts"] += imageChars
		case "tool_result":
			name := f.calls[b.Call]
			if name == "" {
				name = "other tools"
			}
			f.added[resultPiece(name)] += chars(b.Content)
		}
	}
}

func (f *filler) prompt(text string) {
	switch {
	case strings.HasPrefix(text, "Stop hook feedback:") && ours(text):
		f.added[PieceAinterlinked] += float64(len(text))
	default:
		f.added["prompts"] += float64(len(text))
	}
}

// assistant: each block of a reply is an entry of its own, repeating the
// reply's usage. The first entry of a new reply closes the request before.
func (f *filler) assistant(e entry) {
	u := e.Message.Usage
	size := u.In + u.CacheWrite + u.CacheRead
	if size == 0 {
		return // the harness's own notice, not a model's reply
	}
	if e.Message.ID != f.reply {
		f.request(size)
		f.reply, f.out, f.thinking = e.Message.ID, 0, 0
		f.own = map[string]float64{}
		f.fill.Model = e.Message.Model
	}
	f.out, f.thinking = max(f.out, u.Out), max(f.thinking, u.Details.Thinking)
	var blocks []struct {
		Type  string          `json:"type"`
		Text  string          `json:"text"`
		ID    string          `json:"id"`
		Name  string          `json:"name"`
		Input json.RawMessage `json:"input"`
	}
	if json.Unmarshal(e.Message.Content, &blocks) != nil {
		return
	}
	for _, b := range blocks {
		switch b.Type {
		case "text":
			f.own["its own text"] += float64(len(b.Text))
		case "tool_use":
			f.calls[b.ID] = b.Name
			f.own[callPiece(b.Name)] += float64(len(b.Input) + len(b.Name))
		}
	}
}

// request: a request of size tokens was made, with everything added so far.
func (f *filler) request(size int64) {
	fl := &f.fill
	defer func() {
		f.added, f.compacted = map[string]float64{}, false
		fl.Now, fl.Peak = size, max(fl.Peak, size)
	}()
	switch {
	case fl.Start == 0:
		fl.Start = size
		f.begin()
	case f.compacted:
		fl.Compactions[len(fl.Compactions)-1].To = size
	case size > fl.Now:
		grown := float64(size - fl.Now)
		own := min(float64(f.out), grown)
		think := 0.0
		if f.out > 0 {
			think = own * float64(f.thinking) / float64(f.out)
		}
		if think > 0 {
			f.grown["its own thinking"] += think
		}
		if left := share(f.grown, f.own, own-think); left > 0 {
			f.grown["its own text"] += left
		}
		if left := share(f.grown, f.added, grown-own); left > 0 {
			f.grown["other"] += left
		}
	}
}

// begin splits the first request among what it carried.
func (f *filler) begin() {
	fl := &f.fill
	total := 0.0
	for _, n := range f.added {
		total += n
	}
	out := map[string]float64{}
	if !f.shaped {
		// What the transcript leaves out makes up the rest.
		known := min(total/charsPerToken, float64(fl.Start))
		share(out, f.added, known)
		if rest := float64(fl.Start) - known; rest >= 1 {
			out[PieceUnrecorded] = rest
		}
	} else {
		share(out, f.added, float64(fl.Start))
	}
	fl.Begin = pieces(out)
	fl.Tools = scale(fl.Tools, out[PieceTools])
	fl.Files = scale(fl.Files, out[PieceInstructions])
}

// share adds tokens to out in proportion to chars, and returns what no
// chars account for.
func share(out, by map[string]float64, tokens float64) float64 {
	total := 0.0
	for _, n := range by {
		total += n
	}
	if total == 0 {
		return tokens
	}
	for k, n := range by {
		out[k] += tokens * n / total
	}
	return 0
}

// scale turns pieces measured in chars into shares of tokens.
func scale(ps []Piece, tokens float64) []Piece {
	total := 0.0
	for _, p := range ps {
		total += float64(p.Tokens)
	}
	if total == 0 {
		return nil
	}
	out := make([]Piece, 0, len(ps))
	for _, p := range ps {
		out = append(out, Piece{p.Name, int64(float64(p.Tokens) * tokens / total)})
	}
	return out
}

// pieces lists a map's, largest first.
func pieces(m map[string]float64) []Piece {
	out := make([]Piece, 0, len(m))
	for k, n := range m {
		out = append(out, Piece{k, int64(n)})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Tokens != out[j].Tokens {
			return out[i].Tokens > out[j].Tokens
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func resultPiece(tool string) string {
	if p := serverPiece(tool); p != "" {
		return p
	}
	return tool + " results"
}

func callPiece(tool string) string {
	if p := serverPiece(tool); p != "" {
		return p
	}
	return "its own " + tool + " calls"
}

// serverPiece: an MCP server's calls and results count as the server's.
func serverPiece(tool string) string {
	rest, ok := strings.CutPrefix(tool, "mcp__")
	if !ok {
		return ""
	}
	server, _, _ := strings.Cut(rest, "__")
	if server == "interlinked" || server == PieceAinterlinked {
		return PieceAinterlinked
	}
	return "MCP " + server
}

// chars is how much text content (a string, or blocks) puts in the window.
func chars(content json.RawMessage) float64 {
	var s string
	if json.Unmarshal(content, &s) == nil {
		return float64(len(s))
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(content, &blocks) != nil {
		return float64(len(content))
	}
	n := 0.0
	for _, b := range blocks {
		switch b.Type {
		case "text":
			n += float64(len(b.Text))
		case "image", "document":
			n += imageChars
		default:
			n += float64(len(b.Type))
		}
	}
	return n
}

// ClaudeSessions lists Claude Code's transcripts written since, of the
// sessions started in one of dirs (nil: everywhere), latest first.
func ClaudeSessions(dirs []string, since time.Time) []string {
	want := map[string]bool{}
	for _, d := range dirs {
		want[strings.ToLower(projectDir(d))] = true
	}
	type found struct {
		path string
		at   time.Time
	}
	var all []found
	seen := map[string]bool{}
	for _, root := range claudeRoots() {
		matches, _ := filepath.Glob(filepath.Join(root, "projects", "*", "*.jsonl"))
		for _, m := range matches {
			if dirs != nil && !want[strings.ToLower(filepath.Base(filepath.Dir(m)))] {
				continue
			}
			fi, err := os.Stat(m)
			if err != nil || !fi.Mode().IsRegular() || fi.ModTime().Before(since) || seen[m] {
				continue
			}
			seen[m] = true
			all = append(all, found{m, fi.ModTime()})
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].at.After(all[j].at) })
	out := make([]string, 0, len(all))
	for _, f := range all {
		out = append(out, f.path)
	}
	return out
}

// projectDir is the directory Claude Code keeps a project's transcripts in.
func projectDir(path string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			return r
		}
		return '-'
	}, path)
}
