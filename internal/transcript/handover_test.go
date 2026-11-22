package transcript

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

func compacted(min int, text string) string {
	return fmt.Sprintf(`{"type":"user","isCompactSummary":true,"isVisibleInTranscriptOnly":true,"message":{"role":"user","content":%q},"timestamp":%q,"turnOrigin":"human"}`, text, at(min))
}

func said(min int, text string) string {
	return fmt.Sprintf(`{"type":"assistant","message":{"id":"r%d","role":"assistant","content":[{"type":"text","text":%q},{"type":"tool_use","id":"t","name":"Bash","input":{"command":"secret-ish"}}]},"timestamp":%q}`, min, text, at(min))
}

// opened records the working directory, as Claude Code does on its entries.
func opened(dir string) string {
	return fmt.Sprintf(`{"type":"user","isMeta":true,"cwd":%q,"message":{"role":"user","content":"<local-command-caveat/>"},"timestamp":%q}`, dir, at(0))
}

const proj = "/work/shop"

// A session that left hands over Claude Code's own account of the work: the
// last compaction summary without the wrapping meant for that session, the
// user's requests since, and its last report; and one line on where it
// stands.
func TestAHandoverIsTheLastSummaryTheRequestsSinceAndTheLastReport(t *testing.T) {
	summary := "This session is being continued from a previous conversation that ran out of context. The summary below covers the earlier portion of the conversation.\n\n" +
		"<system-reminder>\nhooks said things\n</system-reminder>\nSummary:\n1. Primary Request: build the cart\n8. Current Work:\n   Wiring the checkout button to the payment API.\n" +
		"9. Optional Next Step:\n   Tests.\n\nContinue the conversation from where it left off without asking the user any further questions."
	write(t,
		opened(proj),
		human(0, "an old request, before the summary"),
		said(1, "an old report"),
		compacted(2, "an older summary"),
		compacted(3, summary),
		human(4, "now the tests"),
		said(5, "Tests written; two fail on the tax rounding."),
		`{"type":"assistant","isSidechain":true,"message":{"role":"assistant","content":[{"type":"text","text":"a subagent's words"}]},"timestamp":"`+at(6)+`"}`,
	)
	h, ok := ReadHandover("claude-code", sid, proj)
	if !ok {
		t.Fatal("no handover")
	}
	for _, want := range []string{"build the cart", "Wiring the checkout button", "- now the tests", "two fail on the tax rounding"} {
		if !strings.Contains(h.Body, want) {
			t.Fatalf("missing %q in:\n%s", want, h.Body)
		}
	}
	for _, not := range []string{"older summary", "an old request", "hooks said things", "Continue the conversation", "ran out of context", "secret-ish", "subagent's words"} {
		if strings.Contains(h.Body, not) {
			t.Fatalf("kept %q in:\n%s", not, h.Body)
		}
	}
	if h.Gist != "Wiring the checkout button to the payment API. 9. Optional Next Step: Tests." {
		t.Fatalf("gist %q", h.Gist)
	}
}

// Without a compaction: the first request and the last report. Only a
// transcript that ran in the agent's checkout (or a folder inside it) is
// read: a session id claimed from elsewhere brings nothing in.
func TestAShortSessionHandsOverItsRequestAndLastReport(t *testing.T) {
	write(t, opened(proj+"/web"), human(0, "fix the login redirect"), said(1, "Fixed in auth.go:42; deployed nowhere yet."))
	h, ok := ReadHandover("claude-code", sid, proj)
	if !ok || !strings.Contains(h.Body, "## The first request\n\nfix the login redirect") || h.Gist != "Fixed in auth.go:42; deployed nowhere yet." {
		t.Fatalf("%v %+v", ok, h)
	}
	for _, other := range []string{"/work/bank", "/work/shopfront", "/work/shop/api", ""} {
		if _, ok := ReadHandover("claude-code", sid, other); ok {
			t.Fatalf("read a transcript of %s/web for %q", proj, other)
		}
	}
	if _, ok := ReadHandover("claude-code", "00000000-none-4000-8000-000000000000", proj); ok {
		t.Fatal("a missing transcript handed something over")
	}
	if _, ok := ReadHandover("codex", sid, proj); ok {
		t.Fatal("codex has no transcript reader for handovers yet")
	}
}

func TestATranscriptWithoutAWorkingDirectoryHandsOverNothing(t *testing.T) {
	write(t, human(0, "fix it"), said(1, "fixed"))
	if _, ok := ReadHandover("claude-code", sid, proj); ok {
		t.Fatal("read a transcript that never said where it ran")
	}
}

func TestANearlyEmptySessionHandsOverNothing(t *testing.T) {
	p := write(t, opened(proj), human(0, "hi"))
	if _, ok := ReadHandover("claude-code", sid, proj); ok {
		t.Fatal("handed over a session that never reported")
	}
	_ = os.Remove(p)
}
