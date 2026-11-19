package harness

import (
	"bytes"
	"encoding/json"
	"strings"
)

// Codex's config.toml is edited textually, one table at a time, so the rest
// of the user's file (comments, ordering) is untouched.

func tomlString(s string) string {
	if !strings.ContainsAny(s, "'\n") {
		return "'" + s + "'" // literal string: backslashes in Windows paths stay as-is
	}
	b, _ := json.Marshal(s) // JSON escapes are valid TOML basic-string escapes
	return string(b)
}

func isHeader(line string) bool {
	l := strings.TrimSpace(line)
	return strings.HasPrefix(l, "[") && !strings.HasPrefix(l, "#")
}

func headerName(line string) string {
	l := strings.TrimSpace(line)
	if i := strings.Index(l, "#"); i > 0 {
		l = strings.TrimSpace(l[:i])
	}
	l = strings.TrimPrefix(strings.TrimPrefix(l, "["), "[")
	l = strings.TrimSuffix(strings.TrimSuffix(l, "]"), "]")
	return strings.TrimSpace(l)
}

// tomlTableSpan finds [name] and its sub-tables ([name.x]); returns line indexes.
func tomlTableSpan(lines []string, name string) (int, int) {
	start := -1
	for i, l := range lines {
		if isHeader(l) && headerName(l) == name {
			start = i
			break
		}
	}
	if start < 0 {
		return -1, -1
	}
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		if isHeader(lines[i]) {
			h := headerName(lines[i])
			if h != name && !strings.HasPrefix(h, name+".") {
				end = i
				break
			}
		}
	}
	// Blank lines and comment lines right before the next table belong to
	// that table (a "# my settings" header), not to ours.
	for end > start+1 {
		l := strings.TrimSpace(lines[end-1])
		if l != "" && !strings.HasPrefix(l, "#") {
			break
		}
		end--
	}
	return start, end
}

func tomlHasTable(src, name string) bool {
	s, _ := tomlTableSpan(strings.Split(src, "\n"), name)
	return s >= 0
}

// tomlRemoveTable deletes [name] and its sub-tables.
func tomlRemoveTable(src, name string) (string, bool) {
	crlf := strings.Contains(src, "\r\n")
	lines := strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n")
	s, e := tomlTableSpan(lines, name)
	if s < 0 {
		return src, false
	}
	// Also drop one blank line before the table.
	if s > 0 && strings.TrimSpace(lines[s-1]) == "" {
		s--
	}
	out := strings.Join(append(append([]string{}, lines[:s]...), lines[e:]...), "\n")
	if crlf {
		out = strings.ReplaceAll(out, "\n", "\r\n")
	}
	return out, true
}

// tomlSetTable replaces (or appends) [name] with body.
func tomlSetTable(src, name, body string) string {
	src, _ = tomlRemoveTable(src, name)
	crlf := strings.Contains(src, "\r\n")
	src = strings.ReplaceAll(src, "\r\n", "\n")
	src = strings.TrimRight(src, "\n")
	block := "[" + name + "]\n" + strings.TrimRight(body, "\n") + "\n"
	if src != "" {
		block = "\n\n" + block
	}
	out := src + block
	if crlf {
		out = strings.ReplaceAll(out, "\n", "\r\n")
	}
	return out
}

func jsonMarshal(v any) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	err := enc.Encode(v)
	return strings.TrimSpace(buf.String()), err
}

func jsonIndent(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	err := enc.Encode(v)
	return buf.Bytes(), err
}
