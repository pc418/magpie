package provider

import (
	"context"
	"net/http"
	"runtime"
	"slices"
	"strings"
	"sync"

	"github.com/yetone/magpie/internal/settings"
)

// Settings' "ChatGPT: agent's own prompt" (codexOwnPrompt, off by default):
// a ChatGPT account's request goes with the agent's own instructions in
// place of Codex's, under magpie's name rather than Codex CLI's, and a
// request Codex itself sent keeps Codex's prompt and name. Off, every
// request is made as Codex CLI makes it. The rules are in
// own/codex-own-prompt.md.

// MagpieVersion is this magpie's version, for the User-Agent its requests
// to a ChatGPT account name it by with the setting on.
var MagpieVersion = "dev"

// magpieUserAgent is magpie's own User-Agent to the ChatGPT backend:
// "magpie/v0.1.1294 (darwin; arm64)".
func magpieUserAgent() string {
	return "magpie/" + MagpieVersion + " (" + runtime.GOOS + "; " + runtime.GOARCH + ")"
}

// codexOwnPromptOn reads the setting; a var so tests can count the reads.
var codexOwnPromptOn = func() bool { return settings.Load().CodexOwnPrompt }

type codexCallKey struct{}

// codexCall is how the requests made for one request to magpie go to a
// ChatGPT account: whether the setting is on, read once when first asked
// so its body and its headers (retries too) follow the same answer, and
// whether Codex itself sent it, with the three headers it named itself by.
type codexCall struct {
	on    func() bool
	codex bool
	// Codex's own originator, User-Agent and version, as they came; nil
	// for one it didn't send
	originator, userAgent, version []string
}

// WithCodexCall is ctx carrying how a ChatGPT account's request made for
// the request in came with goes (codexCall): fromCodex says Codex sent it,
// as the gateway judges by its headers. It is no proof and authorizes
// nothing; it only picks whose name the request goes under.
func WithCodexCall(ctx context.Context, in http.Header, fromCodex bool) context.Context {
	c := &codexCall{on: sync.OnceValue(codexOwnPromptOn), codex: fromCodex}
	if fromCodex {
		c.originator = slices.Clone(in.Values("originator"))
		c.userAgent = slices.Clone(in.Values("User-Agent"))
		c.version = slices.Clone(in.Values("version"))
	}
	return context.WithValue(ctx, codexCallKey{}, c)
}

// withMagpieCall is ctx for a request magpie makes of its own accord (a
// model list, a warm-up), never Codex's whoever asked.
func withMagpieCall(ctx context.Context) context.Context { return WithCodexCall(ctx, nil, false) }

func codexCallOf(ctx context.Context) *codexCall {
	c, _ := ctx.Value(codexCallKey{}).(*codexCall)
	return c
}

// ownPrompt is whether the request goes with the agent's own prompt and
// the name below; false for one that carries no codexCall, which goes as
// Codex CLI's.
func (c *codexCall) ownPrompt() bool { return c != nil && c.on() }

// identify names the request in h: Codex's own headers, relayed as Codex
// sent them, on a request it sent, one it didn't send left out; magpie's
// on every other, with no version (a relayed one never stays).
func (c *codexCall) identify(h http.Header) {
	if c.codex {
		for k, vs := range map[string][]string{"originator": c.originator, "User-Agent": c.userAgent, "version": c.version} {
			h.Del(k)
			for _, v := range vs {
				h.Add(k, v)
			}
		}
		if len(c.userAgent) == 0 {
			// an empty value, which Go's transport sends as no User-Agent
			// at all; deleted, it would send its own Go-http-client/1.1
			h.Set("User-Agent", "")
		}
		return
	}
	h.Set("originator", "magpie")
	h.Set("User-Agent", magpieUserAgent())
	h.Del("version")
}

// PrepareFor is Prepare for the request ctx carries: a ChatGPT account's
// body with the agent's own instructions when the setting is on for it.
func (p Provider) PrepareFor(ctx context.Context, body []byte) []byte {
	if p.Account != nil && p.Account.Agent == "codex" && p.Account.body != nil && codexCallOf(ctx).ownPrompt() {
		return codexBodyAs(body, true)
	}
	return p.Prepare(body)
}

// helpfulAssistant is the instructions of a request with none of its own.
const helpfulAssistant = "You are a helpful assistant."

// leadingPrompt is the prompt input starts with: its first n items, the
// system and developer messages before any other item, and their texts
// joined by a blank line. ok is false when the run can't be told as
// instructions as it is: system and developer mixed, or a part that isn't
// text (an image, a file, one magpie doesn't know).
func leadingPrompt(input []any) (text string, n int, ok bool) {
	var texts []string
	role := ""
	ok = true
	for _, item := range input {
		it, _ := item.(map[string]any)
		r, _ := it["role"].(string)
		if it == nil || it["type"] != nil && it["type"] != "message" || r != "system" && r != "developer" {
			break
		}
		n++
		if role == "" {
			role = r
		} else if r != role {
			ok = false
		}
		switch c := it["content"].(type) {
		case string:
			texts = append(texts, c)
		case []any:
			for _, part := range c {
				pt, _ := part.(map[string]any)
				s, isText := pt["text"].(string)
				if pt["type"] != "input_text" || !isText {
					ok = false
					continue
				}
				texts = append(texts, s)
			}
		default:
			ok = false
		}
	}
	return strings.Join(texts, "\n\n"), n, ok && n > 0
}

// agentInstructions is the instructions a request goes with, and its input,
// with the setting on: the client's own; else the prompt its input starts
// with, taken out of the input; "" with the input as it was when that
// prompt can't be told as instructions; helpfulAssistant when it has no
// prompt at all, or one with no text (its empty messages taken out all
// the same). Never Codex's.
func agentInstructions(own string, input []any, prompt string, n int, ok bool) (string, []any) {
	switch {
	case strings.TrimSpace(own) != "":
		return own, input
	case n == 0:
		return helpfulAssistant, input
	case !ok:
		return "", input
	case strings.TrimSpace(prompt) == "":
		return helpfulAssistant, input[n:]
	}
	return prompt, input[n:]
}
