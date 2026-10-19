package gui

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/settings"
)

// Settings' "ChatGPT: agent's own prompt" (own/codex-own-prompt.md), saved
// from the page, is on settings.json for every reader, and stays so
// through a later save that doesn't name it (an older page, another
// setting); the page's own Off turns it off.
func TestCodexOwnPromptSaved(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	save := func(body string) {
		t.Helper()
		rec := httptest.NewRecorder()
		Handler(nil, nil).ServeHTTP(rec, httptest.NewRequest("POST", "/api/settings", strings.NewReader(body)))
		if rec.Code != http.StatusOK {
			t.Fatalf("saving %s: %d %s", body, rec.Code, rec.Body)
		}
	}
	save(`{"theme":"light","codexOwnPrompt":true}`)
	if !settings.Load().CodexOwnPrompt {
		t.Fatal("not saved")
	}
	save(`{"theme":"dark"}`)
	if s := settings.Load(); !s.CodexOwnPrompt || s.Theme != "dark" {
		t.Fatalf("after an unrelated save: codexOwnPrompt %v, theme %q", s.CodexOwnPrompt, s.Theme)
	}
	rec := httptest.NewRecorder()
	Handler(nil, nil).ServeHTTP(rec, httptest.NewRequest("GET", "/api/settings", nil))
	if !strings.Contains(rec.Body.String(), `"codexOwnPrompt":true`) {
		t.Fatalf("the page reads: %s", rec.Body)
	}
	save(`{"theme":"dark","codexOwnPrompt":false}`)
	if settings.Load().CodexOwnPrompt {
		t.Fatal("Off didn't turn it off")
	}
}
