package core

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Search over every task, on a board or archived: SQLite's FTS5 with the
// trigram tokenizer (task_search, migrate.go) finds the candidates, and this
// file decides which match and in what order.
//
// Text is compared folded: lowercase, accents off ("reservacion" finds
// "Reservación"). A query word matches a task when it is a substring of
// the task's title, body, result, labels, assignee or creator ("reserv"
// finds "Reservas"), or, for words of fuzzyMin letters or more, when it is
// one typo away from such a substring: a letter changed, missing, extra,
// or two swapped (nearIndex). Every word of the query must match.
//
// Candidates are the tasks sharing at least one trigram with the query (an
// OR of its trigrams), best bm25 first. Hits are ranked exact ones first,
// then by the share of the query's trigrams the task contains, then open
// before closed before archived, newest first.
//
// Why an edit-distance check rather than a minimum trigram share: a typo
// removes up to three trigrams, so a seven-letter word with one keeps 2 of
// its 5 and a five-letter word as few as 0 of 3; a share low enough to keep
// those keeps noise too. The share still orders the hits. Honest limit: a
// word left with no trigram in common with the task (a five-letter word
// with its middle letter wrong) is found only through the query's other
// words.

const (
	searchLimit      = 20   // hits shown when the caller names no limit
	maxSearchLimit   = 200  // hits shown at most
	searchCandidates = 1000 // tasks the index hands over to be checked
	fuzzyMin         = 5    // letters a word needs to forgive a typo
	maxQueryRunes    = 200
	snipBefore       = 30 // runes of context before a match in a snippet
	snipAfter        = 60
)

// foldPairs maps accented letters to their base letter; fold keeps one
// rune for one rune, so positions in folded text are positions in the
// original.
var foldPairs = []string{
	"àáâãäåāăą", "a", "çćĉċč", "c", "ď", "d", "èéêëēĕėęě", "e", "ĝğġģ", "g", "ĥ", "h",
	"ìíîïĩīĭį", "i", "ĵ", "j", "ķ", "k", "ĺļľ", "l", "ñńņň", "n", "òóôõöōŏő", "o",
	"ŕŗř", "r", "śŝşš", "s", "ţť", "t", "ùúûüũūŭůűų", "u", "ŵ", "w", "ýÿŷ", "y", "źżž", "z",
}

var foldTable = func() map[rune]rune {
	m := map[rune]rune{}
	for i := 0; i < len(foldPairs); i += 2 {
		base, _ := utf8.DecodeRuneInString(foldPairs[i+1])
		for _, r := range foldPairs[i] {
			m[r] = base
		}
	}
	return m
}()

func foldRune(r rune) rune {
	r = unicode.ToLower(r)
	if b, ok := foldTable[r]; ok {
		return b
	}
	return r
}

// fold lowercases s and takes the accents off.
func fold(s string) string { return strings.Map(foldRune, s) }

// queryGrams lists the distinct trigrams of the words, in order.
func queryGrams(words []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, w := range words {
		r := []rune(w)
		for i := 0; i+3 <= len(r); i++ {
			if g := string(r[i : i+3]); !seen[g] {
				seen[g] = true
				out = append(out, g)
			}
		}
	}
	return out
}

// matchExpr is an FTS5 query matching any of the trigrams.
func matchExpr(grams []string) string {
	parts := make([]string, len(grams))
	for i, g := range grams {
		parts[i] = `"` + strings.ReplaceAll(g, `"`, `""`) + `"`
	}
	return strings.Join(parts, " OR ")
}

// taskRef reads a query like "#12" as a task id.
func taskRef(q string) (int64, bool) {
	if !strings.HasPrefix(q, "#") {
		return 0, false
	}
	id, err := strconv.ParseInt(q[1:], 10, 64)
	return id, err == nil && id > 0
}

// SearchTasks finds tasks by any text field; f's board, status, open and
// assignee narrow it as in ListTasks. "#12" reads task 12 wherever it is.
func (s *Store) SearchTasks(ctx context.Context, f TaskFilter) (TaskPage, error) {
	query := []rune(strings.Join(strings.Fields(f.Query), " "))
	page := TaskPage{Query: string(query[:min(len(query), maxQueryRunes)])}
	words := strings.Fields(fold(page.Query))
	if len(words) == 0 {
		return page, errors.New("query is empty")
	}
	if err := s.tx(ctx, s.abandon); err != nil {
		return page, err
	}
	limit := f.Limit
	if limit <= 0 {
		limit = searchLimit
	}
	limit = min(limit, maxSearchLimit)
	q := `SELECT ` + taskCols + `, archived_at FROM ` + foundTasks + ` WHERE 1=1`
	var args []any
	grams := queryGrams(words)
	if id, ok := taskRef(page.Query); ok {
		q += ` AND id=?`
		args = append(args, id)
	} else {
		scope, sargs := s.taskScope(ctx, f)
		q += scope
		args = sargs
		if len(grams) > 0 {
			q += ` AND id IN (SELECT rowid FROM task_search WHERE task_search MATCH ? ORDER BY rank LIMIT ` + strconv.Itoa(searchCandidates) + `)`
			args = append(args, matchExpr(grams))
		} else { // only words under three letters: no trigram to look up
			q += ` ORDER BY id DESC LIMIT ` + strconv.Itoa(5*searchCandidates)
		}
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return page, err
	}
	var hits []hit
	_, byID := taskRef(page.Query)
	for rows.Next() {
		var at int64
		t, err := scanTask(rows, &at)
		if err != nil {
			rows.Close()
			return page, err
		}
		t.ArchivedAt = at
		if byID {
			hits = append(hits, hit{t: t, exact: true})
		} else if h, ok := matchTask(words, grams, t); ok {
			hits = append(hits, h)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return page, err
	}
	rows.Close()
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].before(hits[j]) })
	if len(hits) > limit {
		page.More = len(hits) - limit
		hits = hits[:limit]
	}
	page.Tasks = make([]Task, len(hits))
	for i, h := range hits {
		page.Tasks[i] = h.t
	}
	return page, s.decorate(ctx, page.Tasks)
}

type hit struct {
	t     Task
	exact bool    // every word found as written (folded)
	share float64 // of the query's trigrams found in the task
}

func (h hit) place() int {
	switch {
	case h.t.ArchivedAt > 0:
		return 2
	case h.t.Status == "done" || h.t.Status == "cancelled":
		return 1
	}
	return 0
}

func (h hit) before(o hit) bool {
	switch {
	case h.exact != o.exact:
		return h.exact
	case h.share != o.share:
		return h.share > o.share
	case h.place() != o.place():
		return h.place() < o.place()
	}
	return h.t.ID > o.t.ID
}

// matchTask checks every query word against t's text and, on a match,
// sets t's snippet around the first match in its body or result.
func matchTask(words, grams []string, t Task) (hit, bool) {
	fields := []string{t.Title, t.Body, t.Result, strings.Join(t.Labels, " "), t.Assignee, t.CreatedBy}
	starts := make([]int, len(fields))
	var text []rune
	for i, f := range fields {
		if i > 0 {
			text = append(text, '\n')
		}
		starts[i] = len(text)
		text = append(text, []rune(f)...)
	}
	low := make([]rune, len(text))
	for i, r := range text {
		low[i] = foldRune(r)
	}
	lows := string(low)
	h := hit{t: t, exact: true}
	snipField, snipAt := -1, 0
	for _, w := range words {
		wr := []rune(w)
		at := -1
		if b := strings.Index(lows, w); b >= 0 {
			at = utf8.RuneCountInString(lows[:b])
		} else if len(wr) >= fuzzyMin {
			at = nearIndex(low, wr)
			h.exact = false
		}
		if at < 0 {
			return hit{}, false
		}
		if k := fieldAt(starts, at); snipField < 0 && (k == 1 || k == 2) {
			snipField, snipAt = k, at-starts[k]
		}
	}
	h.share = 1
	if len(grams) > 0 {
		n := 0
		for _, g := range grams {
			if strings.Contains(lows, g) {
				n++
			}
		}
		h.share = float64(n) / float64(len(grams))
	}
	if snipField >= 0 {
		h.t.Snippet = snippet([]rune(fields[snipField]), snipAt)
	}
	return h, true
}

// fieldAt is the index of the field a position of the joined text is in.
func fieldAt(starts []int, at int) int {
	k := 0
	for i, s := range starts {
		if at >= s {
			k = i
		}
	}
	return k
}

// snippet is the text around position at, on one line.
func snippet(field []rune, at int) string {
	from, to := max(0, at-snipBefore), min(len(field), at+snipAfter)
	s := strings.Join(strings.Fields(string(field[from:to])), " ")
	if from > 0 {
		s = "…" + s
	}
	if to < len(field) {
		s += "…"
	}
	return s
}

// nearIndex finds w in text with at most one typo (a letter changed,
// missing, extra, or two neighbours swapped) and returns about where that
// occurrence starts, or -1. It is the edit distance of w against the best
// substring of text ending at each position (Sellers), with swaps counted
// as one edit (optimal string alignment).
func nearIndex(text, w []rune) int {
	m := len(w)
	prev2, prev, cur := make([]int, m+1), make([]int, m+1), make([]int, m+1)
	for i := range prev {
		prev[i], prev2[i] = i, i
	}
	for j := 1; j <= len(text); j++ {
		cur[0] = 0 // a match may start anywhere in text
		for i := 1; i <= m; i++ {
			cost := 1
			if w[i-1] == text[j-1] {
				cost = 0
			}
			cur[i] = min(prev[i-1]+cost, prev[i]+1, cur[i-1]+1)
			if i > 1 && j > 1 && w[i-1] == text[j-2] && w[i-2] == text[j-1] {
				cur[i] = min(cur[i], prev2[i-2]+1)
			}
		}
		if cur[m] <= 1 {
			return max(0, j-m)
		}
		prev2, prev, cur = prev, cur, prev2
	}
	return -1
}
