// Package jsonc edits JSON / JSON-with-comments files in place, changing only
// the bytes of the member being added or removed. Comments, key order and the
// user's formatting everywhere else are preserved — important because we edit
// other tools' config files.
package jsonc

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

type node struct {
	kind    byte // '{', '[', '"', 'l' (literal)
	start   int
	end     int // exclusive
	members []member
	elems   []*node
}

type member struct {
	key      string
	keyStart int
	val      *node
}

type parser struct {
	src []byte
	pos int
}

func (p *parser) ws() {
	for p.pos < len(p.src) {
		c := p.src[p.pos]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == 0xEF || c == 0xBB || c == 0xBF:
			p.pos++
		case c == '/' && p.pos+1 < len(p.src) && p.src[p.pos+1] == '/':
			for p.pos < len(p.src) && p.src[p.pos] != '\n' {
				p.pos++
			}
		case c == '/' && p.pos+1 < len(p.src) && p.src[p.pos+1] == '*':
			end := bytes.Index(p.src[p.pos+2:], []byte("*/"))
			if end < 0 {
				p.pos = len(p.src)
			} else {
				p.pos += end + 4
			}
		default:
			return
		}
	}
}

func (p *parser) errf(format string, a ...any) error {
	line := bytes.Count(p.src[:min(p.pos, len(p.src))], []byte("\n")) + 1
	return fmt.Errorf("line %d: %s", line, fmt.Sprintf(format, a...))
}

func (p *parser) value() (*node, error) {
	p.ws()
	if p.pos >= len(p.src) {
		return nil, p.errf("unexpected end of input")
	}
	switch p.src[p.pos] {
	case '{':
		return p.object()
	case '[':
		return p.array()
	case '"':
		return p.str()
	}
	start := p.pos
	for p.pos < len(p.src) && !strings.ContainsRune(",}] \t\r\n/", rune(p.src[p.pos])) {
		p.pos++
	}
	if p.pos == start {
		return nil, p.errf("unexpected %q", p.src[p.pos])
	}
	return &node{kind: 'l', start: start, end: p.pos}, nil
}

func (p *parser) str() (*node, error) {
	start := p.pos
	p.pos++
	for p.pos < len(p.src) {
		switch p.src[p.pos] {
		case '\\':
			p.pos += 2
			continue
		case '"':
			p.pos++
			return &node{kind: '"', start: start, end: p.pos}, nil
		}
		p.pos++
	}
	return nil, p.errf("unterminated string")
}

func (p *parser) object() (*node, error) {
	n := &node{kind: '{', start: p.pos}
	p.pos++
	for {
		p.ws()
		if p.pos >= len(p.src) {
			return nil, p.errf("unterminated object")
		}
		if p.src[p.pos] == '}' {
			p.pos++
			n.end = p.pos
			return n, nil
		}
		if p.src[p.pos] != '"' {
			return nil, p.errf("expected key, got %q", p.src[p.pos])
		}
		k, err := p.str()
		if err != nil {
			return nil, err
		}
		var key string
		if err := json.Unmarshal(p.src[k.start:k.end], &key); err != nil {
			return nil, p.errf("bad key")
		}
		p.ws()
		if p.pos >= len(p.src) || p.src[p.pos] != ':' {
			return nil, p.errf("expected ':' after %q", key)
		}
		p.pos++
		v, err := p.value()
		if err != nil {
			return nil, err
		}
		n.members = append(n.members, member{key: key, keyStart: k.start, val: v})
		p.ws()
		if p.pos < len(p.src) && p.src[p.pos] == ',' {
			p.pos++
		}
	}
}

func (p *parser) array() (*node, error) {
	n := &node{kind: '[', start: p.pos}
	p.pos++
	for {
		p.ws()
		if p.pos >= len(p.src) {
			return nil, p.errf("unterminated array")
		}
		if p.src[p.pos] == ']' {
			p.pos++
			n.end = p.pos
			return n, nil
		}
		v, err := p.value()
		if err != nil {
			return nil, err
		}
		n.elems = append(n.elems, v)
		p.ws()
		if p.pos < len(p.src) && p.src[p.pos] == ',' {
			p.pos++
		}
	}
}

func parse(src []byte) (*node, error) {
	if len(bytes.TrimSpace(src)) == 0 {
		return nil, nil
	}
	p := &parser{src: src}
	root, err := p.value()
	if err != nil {
		return nil, err
	}
	p.ws()
	if p.pos < len(src) {
		return nil, p.errf("trailing content")
	}
	return root, nil
}

// Valid reports whether src is well-formed JSONC: our structural parse, and
// strict encoding/json once comments and trailing commas are stripped (this
// catches missing commas and bad literals the tolerant parser lets through).
func Valid(src []byte) error {
	if _, err := parse(src); err != nil {
		return err
	}
	if len(bytes.TrimSpace(src)) == 0 {
		return nil
	}
	var v any
	if err := json.Unmarshal(Strip(src), &v); err != nil {
		return fmt.Errorf("not valid JSON: %w", err)
	}
	return nil
}

// Index renders an array path segment.
func Index(i int) string { return "[" + strconv.Itoa(i) + "]" }

func index(seg string) (int, bool) {
	if len(seg) < 3 || seg[0] != '[' || seg[len(seg)-1] != ']' {
		return 0, false
	}
	i, err := strconv.Atoi(seg[1 : len(seg)-1])
	return i, err == nil && i >= 0
}

func (n *node) find(key string) (int, *member) {
	for i := range n.members {
		if n.members[i].key == key {
			return i, &n.members[i]
		}
	}
	return -1, nil
}

func lookup(root *node, path []string) *node {
	cur := root
	for _, k := range path {
		if cur != nil && cur.kind == '[' {
			// Array segment: "[3]".
			i, ok := index(k)
			if !ok || i >= len(cur.elems) {
				return nil
			}
			cur = cur.elems[i]
			continue
		}
		if cur == nil || cur.kind != '{' {
			return nil
		}
		_, m := cur.find(k)
		if m == nil {
			return nil
		}
		cur = m.val
	}
	return cur
}

// Get returns the raw JSONC text at path and whether it exists.
func Get(src []byte, path ...string) ([]byte, bool) {
	root, err := parse(src)
	if err != nil || root == nil {
		return nil, false
	}
	n := lookup(root, path)
	if n == nil {
		return nil, false
	}
	return src[n.start:n.end], true
}

// Unmarshal decodes the value at path (comments and trailing commas allowed).
func Unmarshal(src []byte, v any, path ...string) (bool, error) {
	raw, ok := Get(src, path...)
	if !ok {
		return false, nil
	}
	return true, json.Unmarshal(Strip(raw), v)
}

// Strip removes comments and trailing commas so encoding/json can read it.
func Strip(src []byte) []byte {
	var out bytes.Buffer
	inStr := false
	for i := 0; i < len(src); i++ {
		c := src[i]
		if inStr {
			out.WriteByte(c)
			if c == '\\' && i+1 < len(src) {
				i++
				out.WriteByte(src[i])
			} else if c == '"' {
				inStr = false
			}
			continue
		}
		switch {
		case c == '"':
			inStr = true
			out.WriteByte(c)
		case c == '/' && i+1 < len(src) && src[i+1] == '/':
			for i < len(src) && src[i] != '\n' {
				i++
			}
			out.WriteByte('\n')
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			end := bytes.Index(src[i+2:], []byte("*/"))
			if end < 0 {
				i = len(src)
			} else {
				i += end + 3
			}
		case c == ',':
			p := &parser{src: src, pos: i + 1}
			p.ws() // whitespace and comments
			if p.pos < len(src) && (src[p.pos] == '}' || src[p.pos] == ']') {
				continue // trailing comma
			}
			out.WriteByte(c)
		default:
			out.WriteByte(c)
		}
	}
	return out.Bytes()
}

// --- formatting helpers ---

func lineIndent(src []byte, pos int) string {
	ls := bytes.LastIndexByte(src[:pos], '\n') + 1
	i := ls
	for i < len(src) && (src[i] == ' ' || src[i] == '\t') {
		i++
	}
	return string(src[ls:i])
}

func firstOnLine(src []byte, pos int) bool {
	ls := bytes.LastIndexByte(src[:pos], '\n') + 1
	return len(bytes.TrimSpace(src[ls:pos])) == 0
}

func indentUnit(src []byte) string {
	for _, line := range bytes.Split(src, []byte("\n")) {
		if len(line) > 0 && (line[0] == ' ' || line[0] == '\t') {
			i := 0
			for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
				i++
			}
			if line[0] == '\t' {
				return "\t"
			}
			if i >= 2 {
				return strings.Repeat(" ", min(i, 4))
			}
		}
	}
	return "  "
}

func marshal(v any, prefix, unit string) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent(prefix, unit)
	if err := enc.Encode(v); err != nil {
		return "", err
	}
	return strings.TrimRight(buf.String(), "\n"), nil
}

func multiline(src []byte, n *node) bool { return bytes.IndexByte(src[n.start:n.end], '\n') >= 0 }

func splice(src []byte, from, to int, text string) []byte {
	out := make([]byte, 0, len(src)+len(text))
	out = append(out, src[:from]...)
	out = append(out, text...)
	return append(out, src[to:]...)
}

// encoder renders one item for a given line indent; indent "" with unit ""
// means a compact single-line rendering.
type encoder func(indent, unit string) (string, error)

// insert appends an item at the end of container c, matching its layout.
func insert(src []byte, c *node, encode encoder, count int) ([]byte, error) {
	unit := indentUnit(src)
	if count == 0 {
		base := lineIndent(src, c.start)
		item, err := encode(base+unit, unit)
		if err != nil {
			return nil, err
		}
		inner := src[c.start+1 : c.end-1]
		if len(bytes.TrimSpace(inner)) == 0 { // expand onto its own lines
			return splice(src, c.start+1, c.end-1, "\n"+base+unit+item+"\n"+base), nil
		}
		// The container holds only comments: keep them, put the item first.
		return splice(src, c.start+1, c.start+1, "\n"+base+unit+item), nil
	}
	var lastEnd, firstStart int
	if c.kind == '{' {
		lastEnd, firstStart = c.members[len(c.members)-1].val.end, c.members[0].keyStart
	} else {
		lastEnd, firstStart = c.elems[len(c.elems)-1].end, c.elems[0].start
	}
	if !multiline(src, c) || !firstOnLine(src, firstStart) {
		item, err := encode("", "")
		if err != nil {
			return nil, err
		}
		return splice(src, lastEnd, lastEnd, ", "+item), nil
	}
	ind := lineIndent(src, firstStart)
	item, err := encode(ind, unit)
	if err != nil {
		return nil, err
	}
	return splice(src, lastEnd, lastEnd, ",\n"+ind+item), nil
}

// Set writes value at path, creating intermediate objects as needed.
func Set(src []byte, path []string, value any) ([]byte, error) {
	if len(path) == 0 {
		return nil, errors.New("empty path")
	}
	root, err := parse(src)
	if err != nil {
		return nil, err
	}
	if root == nil {
		src, root = []byte("{}\n"), &node{kind: '{', start: 0, end: 2}
	}
	if root.kind != '{' {
		return nil, errors.New("top level is not an object")
	}
	cur := root
	for i, k := range path {
		_, m := cur.find(k)
		if m == nil {
			// Build the missing tail as nested objects.
			var v any = value
			for j := len(path) - 1; j > i; j-- {
				v = map[string]any{path[j]: v}
			}
			key, _ := json.Marshal(k)
			return insert(src, cur, func(indent, unit string) (string, error) {
				s, err := marshal(v, indent, unit)
				return string(key) + ": " + s, err
			}, len(cur.members))
		}
		if i == len(path)-1 {
			indent, unit := lineIndent(src, m.keyStart), indentUnit(src)
			if !firstOnLine(src, m.keyStart) {
				indent, unit = "", ""
			}
			s, err := marshal(value, indent, unit)
			if err != nil {
				return nil, err
			}
			return splice(src, m.val.start, m.val.end, s), nil
		}
		if m.val.kind != '{' {
			return nil, fmt.Errorf("%s is not an object", strings.Join(path[:i+1], "."))
		}
		cur = m.val
	}
	return src, nil
}

type span struct{ start, end int }

func items(c *node) []span {
	var out []span
	if c.kind == '{' {
		for _, m := range c.members {
			out = append(out, span{m.keyStart, m.val.end})
		}
	} else {
		for _, e := range c.elems {
			out = append(out, span{e.start, e.end})
		}
	}
	return out
}

// remove cuts item i and its own comma, never the comments that belong to
// its neighbours. When the item sat alone on its line(s), the emptied
// line goes too.
func remove(src []byte, c *node, i int) []byte {
	it := items(c)
	start, end := it[i].start, it[i].end
	comma := nextComma(src, end)
	prevComma := -1
	if comma >= 0 {
		end = comma + 1
	} else if i > 0 {
		// Last item without a trailing comma: the separator is the previous
		// item's comma, which must go so no trailing comma is left behind.
		prevComma = nextComma(src, it[i-1].end)
	}
	// Widen to whole lines when nothing else shares them.
	ls := bytes.LastIndexByte(src[:start], '\n') + 1
	le := len(src)
	if k := bytes.IndexByte(src[end:], '\n'); k >= 0 {
		le = end + k + 1
	}
	switch {
	case blank(src[ls:start]) && blank(src[end:le]) && ls > c.start && le <= c.end:
		start, end = ls, le // the item had its line(s) to itself
	case prevComma >= 0 && blank(src[prevComma+1:start]):
		start, prevComma = prevComma, -1 // inline "a, b" → "a"
	case comma >= 0:
		for end < len(src) && (src[end] == ' ' || src[end] == '\t') {
			end++ // inline "a, b, c" → "a, c"
		}
	}
	out := splice(src, start, end, "")
	if prevComma >= 0 && prevComma < start {
		out = splice(out, prevComma, prevComma+1, "")
	}
	return out
}

// nextComma finds the comma right after pos (skipping only whitespace).
func nextComma(src []byte, pos int) int {
	for pos < len(src) && (src[pos] == ' ' || src[pos] == '\t' || src[pos] == '\n' || src[pos] == '\r') {
		pos++
	}
	if pos < len(src) && src[pos] == ',' {
		return pos
	}
	return -1
}

func blank(b []byte) bool { return len(bytes.TrimSpace(b)) == 0 }

// RemoveAt deletes element i of the array at path.
func RemoveAt(src []byte, path []string, i int) ([]byte, error) {
	root, err := parse(src)
	if err != nil {
		return nil, err
	}
	n := lookup(root, path)
	if n == nil || n.kind != '[' || i < 0 || i >= len(n.elems) {
		return src, nil
	}
	return remove(src, n, i), nil
}

// Delete removes the member at path. It reports whether it existed.
func Delete(src []byte, path []string) ([]byte, bool, error) {
	root, err := parse(src)
	if err != nil || root == nil {
		return src, false, err
	}
	parent := lookup(root, path[:len(path)-1])
	if parent == nil || parent.kind != '{' {
		return src, false, nil
	}
	i, _ := parent.find(path[len(path)-1])
	if i < 0 {
		return src, false, nil
	}
	return remove(src, parent, i), true, nil
}

// Append adds value to the array at path, creating it if missing.
func Append(src []byte, path []string, value any) ([]byte, error) {
	root, err := parse(src)
	if err != nil {
		return nil, err
	}
	n := lookup(root, path)
	if n == nil {
		return Set(src, path, []any{value})
	}
	if n.kind != '[' {
		return nil, fmt.Errorf("%s is not an array", strings.Join(path, "."))
	}
	return insert(src, n, func(indent, unit string) (string, error) { return marshal(value, indent, unit) }, len(n.elems))
}

// RemoveIf deletes array elements at path whose raw text satisfies pred.
// It returns the edited source and how many were removed.
func RemoveIf(src []byte, path []string, pred func(raw []byte) bool) ([]byte, int, error) {
	removed := 0
	for {
		root, err := parse(src)
		if err != nil || root == nil {
			return src, removed, err
		}
		n := lookup(root, path)
		if n == nil || n.kind != '[' {
			return src, removed, nil
		}
		hit := -1
		for i, e := range n.elems {
			if pred(src[e.start:e.end]) {
				hit = i
				break
			}
		}
		if hit < 0 {
			return src, removed, nil
		}
		src = remove(src, n, hit)
		removed++
	}
}

// Len returns the number of elements/members at path (-1 if missing).
func Len(src []byte, path ...string) int {
	root, err := parse(src)
	if err != nil || root == nil {
		return -1
	}
	n := lookup(root, path)
	if n == nil {
		return -1
	}
	if n.kind == '{' {
		return len(n.members)
	}
	return len(n.elems)
}
