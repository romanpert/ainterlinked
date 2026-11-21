package hooks

import (
	"strings"
	"testing"
)

// Activity is broadcast to every peer and the dashboard: for a shell command
// it names the first program (and a known verb such as "git commit"), never
// an argument. Cases include every leak found in review.
func TestActivityFromToolNamesOnlyTheProgram(t *testing.T) {
	cases := []struct{ cmd, want string }{
		{`ssh prise-dev 'docker exec prise-mcp-video-1 pip list'`, "Bash: ssh"},
		{`cd "C:/Users/x/proj" && grep -rn TODO src | head -5`, "Bash: grep"},
		{`curl -H "Authorization: Bearer sk-abc123456789" https://api.example.com`, "Bash: curl"},
		{`OPENAI_API_KEY=sk-live-999 python run.py`, "Bash: python"},
		{`git commit -m "secret plan"; go test ./...`, "Bash: git commit"},
		{`go test ./... 2>&1 | tail`, "Bash: go test"},
		{`/usr/local/bin/node scripts/e2e.mjs`, "Bash: node"},
		{`C:\tools\rg.exe pattern`, "Bash: rg"},
		{`yarn prise-dev`, "Bash: yarn"},
		{`npm run build`, "Bash: npm run"},
		// line continuations (bash \ and PowerShell `)
		{"gh secret set API_KEY --body \\\n  sk-live-abc123def456", "Bash: gh"},
		{"ssh \\\n  prise-dev 'uptime'", "Bash: ssh"},
		{"docker run --rm \\\n  -e OPENAI_API_KEY=sk-live-abc123 \\\n  prise-dev-image", "Bash: docker run"},
		{"Copy-Item `\n  prise-secrets.txt `\n  C:\\dest", "Bash: Copy-Item"},
		{"cd \\\n  prise-dev && ls", "Bash: ls"},
		// heredocs and here-strings
		{"cat <<'EOF' | psql\nprise-dev hunter2\nEOF", "Bash: cat"},
		{"cat > f <<EOF && echo done\nprise-dev hunter2\nEOF", "Bash: cat"},
		{"cat<<EOF\nprise-dev secret\nEOF", "Bash: cat"},
		{"python - <<'EOF'\nsecret = 'x'\nprise_dev()\nEOF\nls", "Bash: python"},
		{"$b = @'\nit's prise-dev\nhunter2\n'@", "Bash"},
		// escapes, comments
		{`DB_PASS=abc\ hunter2 psql -h db`, "Bash: psql"},
		{`echo $'it\'s; prise-dev x'`, "Bash: echo"},
		{`echo a\;prise-dev`, "Bash: echo"},
		{`ls # deploy to prise-dev; then hunter2`, "Bash: ls"},
		// wrappers and directory changes
		{`cd prise-dev && ls`, "Bash: ls"},
		{`pushd prise-secret-repo; ls`, "Bash: ls"},
		{`cd My\ Projects && ls`, "Bash: ls"},
		{`sudo -u prise-admin systemctl restart x`, "Bash: sudo"},
		// PowerShell
		{`$env:TOKEN='sk-1abcdefgh'; & 'C:\path\tool.exe' --key prise`, "Bash: tool"},
	}
	for _, c := range cases {
		got := activityFromTool(Input{ToolName: "Bash", ToolInput: map[string]any{"command": c.cmd}}, "")
		if got != c.want {
			t.Errorf("%q\n got %q\nwant %q", c.cmd, got, c.want)
		}
		for _, leak := range []string{"sk-", "Bearer", "secret", "prise", "hunter2", "999", "Projects"} {
			if strings.Contains(got, leak) {
				t.Errorf("%q leaked %q in %q", c.cmd, leak, got)
			}
		}
	}
}

// Prompts are only shared when the user opts in (presence.share_prompts);
// then credential-looking text is redacted.
func TestActivityFromPromptRedactsSecrets(t *testing.T) {
	for _, p := range []string{
		"use the key sk-proj-AbCdEf1234567890xyz to call it",
		"token: ghp_0123456789abcdefABCDEF0123 please",
		"password=hunter2hunter2 then deploy",
		"Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.abc",
		"aws AKIAIOSFODNN7EXAMPLE now",
		"set DB_PASSWORD=hunter2 first",
		"GITHUB_TOKEN=hunter2abc go",
		"client_secret=hunter2xx ok",
		"PGPASSWORD=hunter2 psql",
		`config {"password": "hunter2"}`,
		"pass=hunter2",
		"connect to postgres://admin:hunter2@db.example.com/app",
		"gitlab glpat-hunter2AbCdEfGhIjKl",
	} {
		got := activityFromPrompt(p)
		for _, leak := range []string{"sk-proj-AbCd", "ghp_0123", "hunter2", "eyJhbGci", "AKIAIOSFODNN7"} {
			if strings.Contains(got, leak) {
				t.Errorf("prompt %q leaked %q: %q", p, leak, got)
			}
		}
	}
	for _, p := range []string{
		"fix the login bug in auth.go",
		"add basic auth to the login page",
		"explain the bearer token flow",
		"edit apps/web/src/app/api/v1/users/route.ts",
	} {
		if got := activityFromPrompt(p); got != "on: "+p {
			t.Errorf("plain prompt changed: %q -> %q", p, got)
		}
	}
}

// Truncation counts runes, never cutting a UTF-8 character in half, and a
// huge prompt is clipped before the regexes run.
func TestActivityFromPromptClipsRunes(t *testing.T) {
	got := activityFromPrompt(strings.Repeat("ñ", 200))
	if !strings.HasSuffix(got, "…") || strings.ContainsRune(got, '\uFFFD') {
		t.Errorf("bad clip: %q", got)
	}
	if got := activityFromPrompt(strings.Repeat("basic ", 400000)); len([]rune(got)) > 90 {
		t.Errorf("not clipped: %d runes", len([]rune(got)))
	}
}
