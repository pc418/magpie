package gateway

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// own/codex-own-prompt.md: Settings' codexOwnPrompt through the gateway.

// ownPromptChatGPT signs Codex in to a ChatGPT account whose backend is f,
// with Settings' codexOwnPrompt on.
func ownPromptChatGPT(t *testing.T, f *fake) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	claims := func(m map[string]any) string {
		b, _ := json.Marshal(m)
		return "h." + base64.RawURLEncoding.EncodeToString(b) + ".s"
	}
	os.MkdirAll(filepath.Join(home, ".codex"), 0o755)
	auth := fmt.Sprintf(`{"auth_mode":"chatgpt","tokens":{"id_token":%q,"access_token":%q,"refresh_token":"r","account_id":"acct-1"}}`,
		claims(map[string]any{"email": "me@example.com"}), claims(map[string]any{"exp": time.Now().Add(time.Hour).Unix()}))
	os.WriteFile(filepath.Join(home, ".codex", "auth.json"), []byte(auth), 0o600)
	up := httptest.NewServer(f)
	t.Cleanup(up.Close)
	old := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	t.Cleanup(func() { provider.CodexBase = old })
	if err := settings.Save(settings.Settings{CodexOwnPrompt: true}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = settings.Save(settings.Settings{}) })
}

// postOwn is post with the caller's headers.
func postOwn(t *testing.T, path, body string, header ...string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	New().Handler().ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func upstreamInput(t *testing.T, up map[string]any) []map[string]any {
	t.Helper()
	var out []map[string]any
	b, _ := json.Marshal(up["input"])
	json.Unmarshal(b, &out)
	return out
}

// With the setting on, Pi's turn reaches the ChatGPT account with Pi's
// prompt as the instructions, out of the input, nothing of Codex's, under
// magpie's name with no version.
func TestPiResponsesOwnPromptToChatGPT(t *testing.T) {
	f := &fake{t: t, ctype: "none", reply: piReply}
	ownPromptChatGPT(t, f)
	code, body := postOwn(t, "/v1/responses", fmt.Sprintf(piResponses, "codex/gpt-5.5"), "User-Agent", "pi/0.75.0", "version", "0.170.0")
	up := checkPiRelayed(t, f, code, body)
	if f.path != "/backend-api/codex/responses" || up["instructions"] != "You are Pi." || strings.Contains(string(f.got), "You are Codex") {
		t.Fatalf("relayed: %s %s", f.path, f.got)
	}
	for _, it := range upstreamInput(t, up) {
		if it["role"] == "developer" || it["role"] == "system" {
			t.Fatalf("prompt left in the input: %s", f.got)
		}
	}
	want := "magpie/" + Version + " (" + runtime.GOOS + "; " + runtime.GOARCH + ")"
	if f.head.Get("originator") != "magpie" || f.head.Get("User-Agent") != want || f.head.Values("version") != nil ||
		f.head.Get("chatgpt-account-id") != "acct-1" || !strings.HasPrefix(f.head.Get("Authorization"), "Bearer ") {
		t.Fatalf("headers: %v", f.head)
	}
}

// Claude Code on a GPT model: its system prompt, translated, is the
// instructions as it wrote it.
func TestClaudeCodeOwnPromptToChatGPT(t *testing.T) {
	f := &fake{t: t, ctype: "none", reply: piReply}
	ownPromptChatGPT(t, f)
	code, body := postOwn(t, "/v1/messages", `{"model":"codex/gpt-5.5","max_tokens":1024,"stream":true,
	  "system":[{"type":"text","text":"You are Claude Code, Anthropic's official CLI for Claude."}],
	  "messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`, "User-Agent", "claude-cli/2.1.0 (external, cli)", "anthropic-version", "2023-06-01")
	if code != 200 || !strings.Contains(body, "a cat") {
		t.Fatalf("reply: %d %s", code, body)
	}
	var up map[string]any
	if err := json.Unmarshal(f.got, &up); err != nil {
		t.Fatalf("upstream body: %s", f.got)
	}
	if up["instructions"] != "You are Claude Code, Anthropic's official CLI for Claude." || strings.Contains(string(f.got), "You are Codex") {
		t.Fatalf("instructions: %s", f.got)
	}
	for _, it := range upstreamInput(t, up) {
		if it["role"] == "developer" {
			t.Fatalf("developer message added: %s", f.got)
		}
	}
	if f.head.Get("originator") != "magpie" {
		t.Fatalf("headers: %v", f.head)
	}
}

// A turn Codex itself sent keeps Codex's instructions, and goes with the
// originator, User-Agent and version Codex sent, not raised.
func TestCodexOwnTurnKeepsCodexsName(t *testing.T) {
	f := &fake{t: t, ctype: "none", reply: piReply}
	ownPromptChatGPT(t, f)
	ins := "You are Codex, an agent based on GPT-5. You and the user share one workspace.\n\n# Personality"
	turn := fmt.Sprintf(`{"model":"codex/gpt-5.5","instructions":%q,"stream":true,"store":false,"prompt_cache_key":"019a-thread",
	  "input":[{"type":"message","role":"developer","content":[{"type":"input_text","text":"sandbox: workspace-write"}]},
	    {"type":"message","role":"user","content":[{"type":"input_text","text":"fix the test"}]}]}`, ins)
	ua := "codex_cli_rs/0.150.0 (Mac OS 27.0.1; arm64) Apple_Terminal/455"
	code, body := postOwn(t, "/v1/responses", turn, "User-Agent", ua, "originator", "codex_cli_rs", "version", "0.150.0")
	if code != 200 || !strings.Contains(body, "a cat") {
		t.Fatalf("reply: %d %s", code, body)
	}
	var up map[string]any
	json.Unmarshal(f.got, &up)
	if up["instructions"] != ins {
		t.Fatalf("instructions: %s", f.got)
	}
	if in := upstreamInput(t, up); len(in) != 2 || in[0]["role"] != "developer" {
		t.Fatalf("input: %s", f.got)
	}
	if f.head.Get("originator") != "codex_cli_rs" || f.head.Get("User-Agent") != ua || f.head.Get("version") != "0.150.0" {
		t.Fatalf("headers: %v", f.head)
	}
}

// A Codex that names itself by its originator alone, with no User-Agent:
// none reaches the backend, not even Go's own Go-http-client/1.1.
func TestCodexOwnTurnWithoutUserAgentSendsNone(t *testing.T) {
	f := &fake{t: t, ctype: "none", reply: piReply}
	ownPromptChatGPT(t, f)
	code, body := postOwn(t, "/v1/responses", fmt.Sprintf(piResponses, "codex/gpt-5.5"), "originator", "codex_exec")
	if code != 200 || !strings.Contains(body, "a cat") {
		t.Fatalf("reply: %d %s", code, body)
	}
	if f.head.Get("originator") != "codex_exec" || f.head.Values("User-Agent") != nil || f.head.Values("version") != nil {
		t.Fatalf("headers: %v", f.head)
	}
}

// One request reads the setting once: asked again (its structured output
// refused, the gateway asks once more), it goes as it went first, though
// the setting was turned off between the two.
func TestCodexOwnPromptHeldAcrossAnAskAgain(t *testing.T) {
	var mu sync.Mutex
	var heads []http.Header
	var bodies []map[string]any
	up := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var m map[string]any
		json.Unmarshal(b, &m)
		mu.Lock()
		heads, bodies = append(heads, r.Header.Clone()), append(bodies, m)
		first := len(heads) == 1
		mu.Unlock()
		if first {
			// turned off while the request is on its way
			if err := settings.Save(settings.Settings{}); err != nil {
				t.Error(err)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(400)
			io.WriteString(w, `{"error":{"message":"text.format type 'json_schema' is not supported, only 'text' and 'json_object' are allowed","type":"invalid_request_error"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, piReply)
	})
	ownPromptChatGPT(t, &fake{t: t})
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	provider.CodexBase = srv.URL + "/backend-api/codex"

	turn := strings.Replace(fmt.Sprintf(piResponses, "codex/gpt-5.5"), `"max_output_tokens":32000`,
		`"max_output_tokens":32000,"text":{"format":{"type":"json_schema","name":"answer","schema":{"type":"object","properties":{"a":{"type":"string"}}}}}`, 1)
	code, body := postOwn(t, "/v1/responses", turn, "User-Agent", "pi/0.75.0")
	if code != 200 || !strings.Contains(body, "a cat") {
		t.Fatalf("reply: %d %s", code, body)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(heads) != 2 {
		t.Fatalf("%d requests", len(heads))
	}
	// the second carries the schema in its instructions (formatInBody),
	// which rule 1 keeps as they are, Pi's prompt where Pi put it; off,
	// it would have gone with Codex's prompt and name
	for i := range heads {
		b, _ := json.Marshal(bodies[i])
		if heads[i].Get("originator") != "magpie" || strings.Contains(string(b), "You are Codex") || !strings.Contains(string(b), "You are Pi.") {
			t.Fatalf("request %d: %v %s", i+1, heads[i], b)
		}
	}
	if bodies[0]["instructions"] != "You are Pi." || !strings.HasPrefix(bodies[1]["instructions"].(string), "Respond with a single JSON value") {
		t.Fatalf("instructions: %q, %q", bodies[0]["instructions"], bodies[1]["instructions"])
	}
	if settings.Load().CodexOwnPrompt {
		t.Fatal("the setting wasn't turned off")
	}
}
