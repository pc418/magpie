package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/settings"
)

// own/codex-own-prompt.md: Settings' codexOwnPrompt, the agent's own
// instructions and magpie's name on a ChatGPT account's requests.

// ownPromptSetting saves Settings' codexOwnPrompt in the test's home.
func ownPromptSetting(t *testing.T, on bool) {
	t.Helper()
	if err := settings.Save(settings.Settings{CodexOwnPrompt: on}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = settings.Save(settings.Settings{}) })
}

// piTurn is a turn as Pi's openai-responses API sends it (pi-ai's
// openai-responses.js; the gateway's piResponses): its system prompt a
// developer message, no instructions.
const piTurn = `{"model":"gpt-5.5","stream":true,"store":false,"prompt_cache_key":"pi-session-1",
  "input":[
    {"role":"developer","content":"You are Pi."},
    {"role":"user","content":[{"type":"input_text","text":"what is this?"},{"type":"input_image","detail":"auto","image_url":"data:image/png;base64,iVBORw0KGgo="}]},
    {"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"enc-1"},
    {"type":"message","role":"assistant","content":[{"type":"output_text","text":"Let me look.","annotations":[]}],"status":"completed","id":"msg_1"},
    {"type":"function_call","id":"fc_1","call_id":"call_1","name":"read","arguments":"{\"path\":\"a.png\"}"},
    {"type":"function_call_output","call_id":"call_1","output":"PNG"}],
  "tools":[{"type":"function","name":"read","description":"Read a file","parameters":{"type":"object","properties":{"path":{"type":"string"}}},"strict":false}],
  "reasoning":{"effort":"high","summary":"auto"},"include":["reasoning.encrypted_content"],"max_output_tokens":32000}`

// codexFirstLine is how Codex CLI 0.163.0's instructions for gpt-5.6-sol
// start (its models_cache.json, codex-prompts.json).
const codexFirstLine = "You are Codex, an agent based on GPT-5. You and the user share one workspace, and your job is to collaborate with them until their goal is genuinely handled."

// codexTurn is a turn Codex CLI sends: its instructions, then its
// permissions as a developer message and the environment as a user one.
var codexTurn = `{"model":"gpt-5.5","instructions":` + jsonString(codexFirstLine+"\n\n# Personality\n\nAs Codex, you are an excellent communicator.") + `,
  "input":[
    {"type":"message","role":"developer","content":[{"type":"input_text","text":"<permissions instructions>\nFilesystem sandboxing: workspace-write.\n</permissions instructions>"}]},
    {"type":"message","role":"user","content":[{"type":"input_text","text":"<environment_context>\n  <cwd>/Users/me/app</cwd>\n</environment_context>"}]},
    {"type":"message","role":"user","content":[{"type":"input_text","text":"fix the test"}]}],
  "tools":[],"tool_choice":"auto","parallel_tool_calls":false,"reasoning":{"effort":"medium","summary":"auto"},
  "store":false,"stream":true,"include":["reasoning.encrypted_content"],"prompt_cache_key":"019a-thread"}`

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

type sentBody struct {
	Instructions *string          `json:"instructions"`
	Key          string           `json:"prompt_cache_key"`
	Input        []map[string]any `json:"input"`
}

func ownBody(t *testing.T, body string) (sentBody, []byte) {
	t.Helper()
	out := codexBodyAs([]byte(body), true)
	var m sentBody
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if m.Instructions == nil {
		t.Fatalf("no instructions: %s", out)
	}
	return m, out
}

func roles(in []map[string]any) string {
	var out []string
	for _, it := range in {
		s, _ := it["type"].(string)
		if r, _ := it["role"].(string); r != "" {
			s += ":" + r
		}
		out = append(out, s)
	}
	return strings.Join(out, ",")
}

// With the setting on, the body carries the agent's own instructions,
// never Codex's: the client's when it sent some, else the prompt its input
// starts with, lifted out of the input; "" when that prompt isn't text
// alone of one role; a plain line when there is none at all.
func TestCodexOwnPromptInstructions(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CODEX_HOME", dir)
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	writeFile(t, dir+"/models_cache.json", map[string]any{"models": []any{map[string]any{"slug": "gpt-5.5",
		"model_messages": map[string]any{"instructions_template": codexFirstLine + "\nMore."}}}})

	// rule 2: Pi's developer message is the instructions, and leaves the
	// input; the rest of the turn as it was
	m, out := ownBody(t, piTurn)
	if *m.Instructions != "You are Pi." || strings.Contains(string(out), "You are Codex") {
		t.Fatalf("Pi: %s", out)
	}
	if got := roles(m.Input); got != ":user,reasoning,message:assistant,function_call,function_call_output" {
		t.Fatalf("Pi's input: %s\n%s", got, out)
	}
	if m.Key != "pi-session-1" {
		t.Fatalf("Pi's own key: %s", out)
	}

	// rule 1: a translated client's (Claude Code's system prompt, through
	// the Anthropic API) instructions, kept exactly, the input untouched
	claude := `{"model":"gpt-5.5","instructions":"You are Claude Code, Anthropic's official CLI for Claude.\n\nBe brief.  ",
	  "input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}],"stream":true}`
	m, out = ownBody(t, claude)
	if *m.Instructions != "You are Claude Code, Anthropic's official CLI for Claude.\n\nBe brief.  " || roles(m.Input) != "message:user" {
		t.Fatalf("translated: %s", out)
	}

	// rule 1: Codex CLI's own turn, its instructions and developer message
	// as Codex sent them
	m, out = ownBody(t, codexTurn)
	if !strings.HasPrefix(*m.Instructions, codexFirstLine+"\n\n# Personality") || strings.Contains(*m.Instructions, "More.") {
		t.Fatalf("Codex's: %s", out)
	}
	if b, _ := json.Marshal(m.Input[0]["content"]); roles(m.Input) != "message:developer,message:user,message:user" || !strings.Contains(string(b), "permissions instructions") {
		t.Fatalf("Codex's input: %s", out)
	}

	// rule 2: a run of parts and messages, joined by a blank line; a
	// developer message later on stays where it is; blank instructions
	// count as none
	parts := `{"model":"gpt-5.5","instructions":"  ","input":[
	  {"type":"message","role":"system","content":[{"type":"input_text","text":"You are OpenCode."},{"type":"input_text","text":"Use tools."}]},
	  {"type":"message","role":"system","content":"Today is Monday."},
	  {"type":"message","role":"user","content":"hi"},
	  {"type":"message","role":"developer","content":"Mode: plan."}]}`
	m, out = ownBody(t, parts)
	if *m.Instructions != "You are OpenCode.\n\nUse tools.\n\nToday is Monday." || roles(m.Input) != "message:user,message:developer" {
		t.Fatalf("parts: %s", out)
	}

	// rule 3: an image in the leading developer message, or system and
	// developer mixed: all of it stays in the input, instructions ""
	image := `{"model":"gpt-5.5","input":[
	  {"role":"developer","content":[{"type":"input_text","text":"You are Pi."},{"type":"input_image","image_url":"data:image/png;base64,iVBORw0KGgo="}]},
	  {"role":"user","content":"what is it?"}]}`
	m, out = ownBody(t, image)
	if *m.Instructions != "" || roles(m.Input) != ":developer,:user" || !strings.Contains(string(out), "You are Pi.") || !strings.Contains(string(out), "iVBORw0KGgo=") {
		t.Fatalf("image: %s", out)
	}
	mixed := `{"model":"gpt-5.5","input":[
	  {"type":"message","role":"system","content":"You are Aider."},
	  {"type":"message","role":"developer","content":"Edit in place."},
	  {"type":"message","role":"user","content":"go"}]}`
	m, out = ownBody(t, mixed)
	if *m.Instructions != "" || roles(m.Input) != "message:developer,message:developer,message:user" ||
		!strings.Contains(string(out), "You are Aider.") || !strings.Contains(string(out), "Edit in place.") {
		t.Fatalf("mixed: %s", out)
	}

	// rule 4: no prompt at all
	m, out = ownBody(t, `{"model":"gpt-5.5","input":"hi"}`)
	if *m.Instructions != "You are a helpful assistant." || roles(m.Input) != "message:user" {
		t.Fatalf("none: %s", out)
	}

	// rule 5: the key a client didn't name is the one it gets with the
	// setting off, from its prompt as it sent it
	keyless := strings.Replace(piTurn, `"prompt_cache_key":"pi-session-1",`, "", 1)
	var off sentBody
	json.Unmarshal(codexBody([]byte(keyless)), &off)
	m, out = ownBody(t, keyless)
	if m.Key == "" || m.Key != off.Key {
		t.Fatalf("key %q, off %q: %s", m.Key, off.Key, out)
	}

	// the rest of the shaping is codexBody's
	if !strings.Contains(string(out), `"store":false`) || strings.Contains(string(out), "max_output_tokens") || strings.Contains(string(out), `"id":"rs_1"`) {
		t.Fatalf("shaping: %s", out)
	}
}

// With the setting off, a request carrying a codexCall is made exactly as
// one without: the same bytes, the same headers.
func TestCodexOwnPromptOffIsCodexCLIs(t *testing.T) {
	signIn(t)
	ownPromptSetting(t, false)
	p, _ := find(All(), "codex")
	for _, codex := range []bool{false, true} {
		in := http.Header{"User-Agent": {"codex_cli_rs/0.1.0 (Mac OS 26.6.0; arm64) kitty"}, "Originator": {"codex_cli_rs"}, "Version": {"0.1.0"}}
		ctx := WithCodexCall(context.Background(), in, codex)
		for _, body := range []string{piTurn, codexTurn, `{"model":"gpt-5.5","input":"hi"}`} {
			got, want := p.PrepareFor(ctx, []byte(body)), p.Prepare([]byte(body))
			if !bytes.Equal(got, want) {
				t.Fatalf("body:\n%s\nwant\n%s", got, want)
			}
			sign := func(ctx context.Context) http.Header {
				req, _ := http.NewRequest("POST", p.Responses+"/responses", nil)
				req.Header.Set("version", "0.1.0")
				if err := p.Sign(ctx, req, Responses, want); err != nil {
					t.Fatal(err)
				}
				return req.Header
			}
			if g, w := sign(ctx), sign(context.Background()); !equalHeaders(g, w) {
				t.Fatalf("headers %v, want %v", g, w)
			}
		}
	}
}

// With the setting on, a request names magpie, with no version, whatever
// was relayed onto it; one Codex sent goes with Codex's own originator,
// User-Agent and version as it sent them, a missing one left out, and
// the account's sign-in and session either way.
func TestCodexOwnPromptIdentity(t *testing.T) {
	signIn(t)
	ownPromptSetting(t, true)
	was := MagpieVersion
	MagpieVersion = "v0.1.1294"
	t.Cleanup(func() { MagpieVersion = was })
	p, _ := find(All(), "codex")
	sign := func(ctx context.Context) http.Header {
		req, _ := http.NewRequest("POST", p.Responses+"/responses", nil)
		// what the gateway put on before signing: its User-Agent, and
		// Codex's version relayed (codexHeader)
		req.Header.Set("User-Agent", "magpie/v0.1.1294")
		req.Header.Set("version", "0.170.0")
		if err := p.Sign(ctx, req, Responses, []byte(`{"prompt_cache_key":"thread-1"}`)); err != nil {
			t.Fatal(err)
		}
		return req.Header
	}
	account := func(h http.Header) {
		t.Helper()
		if !strings.HasPrefix(h.Get("Authorization"), "Bearer h.") || h.Get("chatgpt-account-id") != "acct-1" ||
			h.Get("session-id") != "thread-1" || h.Get("OpenAI-Beta") != "responses=experimental" {
			t.Fatalf("account's headers: %v", h)
		}
	}

	pi := http.Header{"User-Agent": {"pi/0.75.0"}}
	h := sign(WithCodexCall(context.Background(), pi, false))
	account(h)
	want := "magpie/v0.1.1294 (" + runtime.GOOS + "; " + runtime.GOARCH + ")"
	if h.Get("originator") != "magpie" || h.Get("User-Agent") != want || h.Values("version") != nil {
		t.Fatalf("magpie's name: %v", h)
	}

	codex := http.Header{"User-Agent": {"codex_cli_rs/0.163.0 (Mac OS 27.0.1; arm64) Apple_Terminal/455"}, "Originator": {"codex_cli_rs"}, "Version": {"0.163.0"}}
	h = sign(WithCodexCall(context.Background(), codex, true))
	account(h)
	if h.Get("originator") != "codex_cli_rs" || h.Get("User-Agent") != codex.Get("User-Agent") || h.Get("version") != "0.163.0" {
		t.Fatalf("Codex's own: %v", h)
	}

	// Codex Desktop names no version: none goes, the relayed one neither
	desktop := http.Header{"User-Agent": {"Codex Desktop/0.162.3 (Mac OS 26.6.0; arm64)"}, "Originator": {"Codex Desktop"}}
	h = sign(WithCodexCall(context.Background(), desktop, true))
	if h.Get("originator") != "Codex Desktop" || h.Get("User-Agent") != desktop.Get("User-Agent") || h.Values("version") != nil {
		t.Fatalf("Codex Desktop's own: %v", h)
	}
}

// The setting is read once a request: its body and every signing of it
// (a retry's too) follow that one answer, even when it changes between.
func TestCodexOwnPromptReadOnceARequest(t *testing.T) {
	signIn(t)
	ownPromptSetting(t, true)
	reads := 0
	was := codexOwnPromptOn
	codexOwnPromptOn = func() bool { reads++; return was() }
	t.Cleanup(func() { codexOwnPromptOn = was })
	p, _ := find(All(), "codex")
	ctx := WithCodexCall(context.Background(), http.Header{"User-Agent": {"pi/0.75.0"}}, false)
	body := p.PrepareFor(ctx, []byte(piTurn))
	ownPromptSetting(t, false) // turned off while the request is on its way
	for range 2 {
		req, _ := http.NewRequest("POST", p.Responses+"/responses", nil)
		if err := p.Sign(ctx, req, Responses, body); err != nil {
			t.Fatal(err)
		}
		if req.Header.Get("originator") != "magpie" {
			t.Fatalf("signed as %v", req.Header)
		}
	}
	if !strings.Contains(string(body), `"instructions":"You are Pi."`) || reads != 1 {
		t.Fatalf("%d reads: %s", reads, body)
	}
}

// magpie's own requests, a warm-up and a model list, go under its name
// with the setting on, whoever's request led to them; the warm-up with the
// plain line for its instructions, the list still asked for Codex's
// client_version.
func TestCodexOwnPromptMagpiesOwnRequests(t *testing.T) {
	signIn(t)
	ownPromptSetting(t, true)
	var mu sync.Mutex
	type seen struct {
		path, query string
		h           http.Header
		body        map[string]any
	}
	var got []seen
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var m map[string]any
		json.Unmarshal(b, &m)
		mu.Lock()
		got = append(got, seen{r.URL.Path, r.URL.RawQuery, r.Header.Clone(), m})
		mu.Unlock()
		if strings.HasSuffix(r.URL.Path, "/models") {
			io.WriteString(w, `{"models":[{"slug":"gpt-5.5","display_name":"GPT-5.5","visibility":"list","priority":1}]}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "event: response.completed\ndata: {\"type\":\"response.completed\"}\n\n")
	}))
	defer fake.Close()
	old := CodexBase
	CodexBase = fake.URL + "/backend-api/codex"
	t.Cleanup(func() { CodexBase = old })
	p, _ := find(All(), "codex")
	sign := func(ctx context.Context, req *http.Request, body []byte) error {
		return p.Sign(ctx, req, Responses, body)
	}

	// asked on a request Codex sent: still magpie's own
	codex := WithCodexCall(context.Background(), http.Header{"User-Agent": {"codex_cli_rs/0.163.0"}, "Originator": {"codex_cli_rs"}, "Version": {"0.163.0"}}, true)
	if err := warmCodex(codex, sign, "gpt-5.5", "low"); err != nil {
		t.Fatal(err)
	}
	if _, err := codexModels(codex, sign); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("%d requests", len(got))
	}
	for _, s := range got {
		if s.h.Get("originator") != "magpie" || !strings.HasPrefix(s.h.Get("User-Agent"), "magpie/") || s.h.Values("version") != nil {
			t.Fatalf("%s: %v", s.path, s.h)
		}
	}
	if w := got[0]; w.path != "/backend-api/codex/responses" || w.body["instructions"] != "You are a helpful assistant." || w.body["store"] != false {
		t.Fatalf("warm-up: %+v", w)
	}
	if l := got[1]; l.path != "/backend-api/codex/models" || !strings.HasPrefix(l.query, "client_version=") {
		t.Fatalf("model list: %+v", l)
	}
}

// PIN owner 2026-10-19: codexOwnPrompt is one Settings switch, off by
// default; on, a ChatGPT account's request carries the agent's own prompt
// and names magpie (identity 1b), with no fallback to Codex's. See
// own/codex-own-prompt.md.
func TestPinCodexOwnPromptOwnerDecision(t *testing.T) {
	signIn(t)
	b, _ := json.Marshal(settings.Settings{CodexOwnPrompt: true})
	if !strings.Contains(string(b), `"codexOwnPrompt":true`) {
		t.Fatalf("key: %s", b)
	}
	p, _ := find(All(), "codex")
	ask := func() (string, http.Header) {
		ctx := WithCodexCall(context.Background(), http.Header{"User-Agent": {"pi/0.75.0"}}, false)
		body := p.PrepareFor(ctx, []byte(piTurn))
		req, _ := http.NewRequest("POST", p.Responses+"/responses", nil)
		if err := p.Sign(ctx, req, Responses, body); err != nil {
			t.Fatal(err)
		}
		var m sentBody
		json.Unmarshal(body, &m)
		ins := ""
		if m.Instructions != nil {
			ins = *m.Instructions
		}
		return ins, req.Header
	}
	// off by default: Codex's prompt and name
	if ins, h := ask(); ins == "You are Pi." || h.Get("originator") != "codex_cli_rs" {
		t.Fatalf("default: %q %v", ins, h)
	}
	ownPromptSetting(t, true)
	if ins, h := ask(); ins != "You are Pi." || h.Get("originator") != "magpie" {
		t.Fatalf("on: %q %v", ins, h)
	}
}
