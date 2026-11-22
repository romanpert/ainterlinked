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
	h, ok := ReadHandover("claude-code", sid)
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

// Without a compaction: the first request and the last report, with the
// folder the session first recorded (the hub binds the handover to that
// checkout).
func TestAShortSessionHandsOverItsRequestAndLastReport(t *testing.T) {
	write(t, opened(proj+"/web"), opened("/elsewhere"), human(0, "fix the login redirect"), said(1, "Fixed in auth.go:42; deployed nowhere yet."))
	h, ok := ReadHandover("claude-code", sid)
	if !ok || !strings.Contains(h.Body, "## The first request\n\nfix the login redirect") || h.Gist != "Fixed in auth.go:42; deployed nowhere yet." {
		t.Fatalf("%v %+v", ok, h)
	}
	if h.Cwd != proj+"/web" {
		t.Fatalf("cwd %q", h.Cwd)
	}
	if _, ok := ReadHandover("claude-code", "00000000-none-4000-8000-000000000000"); ok {
		t.Fatal("a missing transcript handed something over")
	}
	if _, ok := ReadHandover("codex", sid); ok {
		t.Fatal("codex has no transcript reader for handovers yet")
	}
}

// Secrets are hidden before anything is cut: a token the cut would leave
// too short to recognise doesn't slip through.
func TestAHandoverHidesSecretsBeforeCuttingThem(t *testing.T) {
	report := strings.Repeat("a ", (maxReport-10)/2) + "ghp_" + strings.Repeat("Ab3", 10)
	write(t, opened(proj), human(0, "use token ghp_"+strings.Repeat("Zz9", 10)), said(1, report))
	h, ok := ReadHandover("claude-code", sid)
	if !ok || strings.Contains(h.Body, "ghp_") || strings.Contains(h.Gist, "ghp_") {
		t.Fatalf("%v %+v", ok, h)
	}
}

func TestATranscriptWithoutAWorkingDirectoryHandsOverNothing(t *testing.T) {
	write(t, human(0, "fix it"), said(1, "fixed"))
	if _, ok := ReadHandover("claude-code", sid); ok {
		t.Fatal("read a transcript that never said where it ran")
	}
}

func TestANearlyEmptySessionHandsOverNothing(t *testing.T) {
	p := write(t, opened(proj), human(0, "hi"))
	if _, ok := ReadHandover("claude-code", sid); ok {
		t.Fatal("handed over a session that never reported")
	}
	_ = os.Remove(p)
}

// What the harness says in the model's place when a request fails (an API
// error, an account's notice) isn't the session's report.
func TestTheHarnessOwnNoticesAreNotTheReport(t *testing.T) {
	notice := `{"type":"assistant","message":{"id":"e1","model":"<synthetic>","role":"assistant","content":[{"type":"text","text":"Can't use your subscription, pay or renew at https://example.com"}],"usage":{"input_tokens":0}},"timestamp":"` + at(3) + `"}`
	write(t, opened(proj), human(0, "fix the login"), said(1, "Half done: auth.go:42."), human(2, "go on"), notice)
	h, ok := ReadHandover("claude-code", sid)
	if !ok || h.Gist != "Half done: auth.go:42." || strings.Contains(h.Body, "subscription") {
		t.Fatalf("%v %+v", ok, h)
	}
	write(t, opened(proj), human(0, "fix the login"), notice)
	if h, ok := ReadHandover("claude-code", sid); ok {
		t.Fatalf("it reported nothing: %+v", h)
	}
}
