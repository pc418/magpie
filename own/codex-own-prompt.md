# ChatGPT: agent's own prompt (`codexOwnPrompt`)

A setting of this build of magpie (the `own` branch), not of upstream
magpie. It is in Settings › Models › Replies, right after "Short Claude
system prompt", and is **off by default**.

Upstream magpie makes every request to a ChatGPT subscription (the Codex
account) the request Codex CLI would send: Codex's ~20 KB system prompt as
`instructions`, the agent's own prompt demoted to a developer message in
`input`, and Codex CLI's `originator`, `User-Agent` and `version` headers.

With the setting **on**, an agent relayed onto a ChatGPT plan (Pi, OpenCode,
Claude Code, …) is answered with its own prompt, and magpie's own requests
name magpie instead of Codex CLI. A request Codex itself sends keeps Codex's
prompt and name.

With the setting **off**, every request is exactly what upstream sends.

## The instructions (`codexBody`)

With the setting on, the body of a request to a ChatGPT account follows
these rules, in order:

1. **The client sent non-blank `instructions`:** they are kept exactly. No
   Codex prompt is put in, no developer message is added, and `input` is
   left as it is. Codex's own requests pass through this way; so do
   clients translated from Chat Completions or the Anthropic API, whose
   system prompt arrives as `instructions`.
2. **No or blank `instructions`:** the run of `system`/`developer` message
   items that `input` starts with (up to the first item of any other kind)
   becomes the `instructions`, when every item's content is a string or a
   list made only of `input_text` parts. The texts are kept exactly and
   joined with a blank line (`"\n\n"`), with no labels or wrappers, and
   those items leave `input`. Pi's turn, which sends its prompt as the first
   developer message, goes with `instructions: "You are Pi."`.
3. **A leading run that can't be told as text faithfully** (an image, a
   file or a part magpie doesn't know, or `system` mixed with `developer`):
   the whole run stays in `input`, and `instructions` is `""`. Nothing falls
   back to Codex's prompt and nothing is dropped.
4. **No instruction content at all** (for example `input: "hi"`):
   `instructions` is `You are a helpful assistant.`
5. **The cache key** (`prompt_cache_key`) is computed as before, from the
   client's own instructions and input before anything is lifted; a key the
   client sent is kept.

Only the leading run is lifted: a developer message later in the
conversation stays where it is. Everything else the body is given (no
storage, streaming, `include`, `tool_choice`, the fields the backend refuses)
is as before.

## The name (`codexSign`)

| Request | Name it goes under, setting on |
| --- | --- |
| `/responses` and `/responses/compact` from Pi or any other caller that isn't Codex | magpie |
| magpie's own model list fetch, for the active and the saved accounts | magpie (the `client_version` query parameter stays) |
| the warm-up that starts an account's window | magpie, with rule 4's instructions |
| a request Codex itself sent through magpie (turns, compaction, web search) | Codex's own, relayed |
| usage (`/wham/usage`) | unchanged |
| the OAuth sign-in | unchanged |

- **magpie's name:** `originator: magpie`, `User-Agent: magpie/<version>
  (<os>; <arch>)`, and no `version` header (one relayed from the caller is
  removed).
- **Codex's own:** the `originator`, `User-Agent` and `version` Codex sent
  go on as they came, not regenerated or raised to a newer version; one it
  didn't send stays missing. Authorization, the account id and the session
  headers are the account's, as before.
- The gateway decides "sent by Codex" from the request's own headers when
  it arrives (`fromCodex`: a User-Agent or originator naming Codex). It is a
  heuristic, used only to pick the name; it authorizes nothing. Non-blank
  `instructions` don't make a caller Codex.
- **One read per request:** the setting is read once for a request, and that
  one answer decides both its body and its headers, retries included.
  Requests magpie makes outside the gateway (the account Test button) go as
  upstream's.
- **A model refused under magpie's name** is not retried under Codex's
  name, prompt, another model or another sign-in: the backend's error is
  shown, and the user picks another model or turns the setting off.

## Where it is

- Setting: `CodexOwnPrompt` in `internal/settings/settings.go`.
- Body and name: `internal/provider/codex_own.go`, used by `codexBodyAs`
  and `codexSign` in `internal/provider/codex_request.go`, the warm-up
  (`warmCodex`) and the model list (`codexModels`).
- Per-request value: the gateway's `codexCalled` middleware
  (`internal/gateway/codex_own.go`) and `Provider.PrepareFor`.
- GUI: the `codexOwnRow` in `internal/gui/assets/app.js`, with its strings
  in `internal/gui/assets/i18n.js`.
- Tests: `internal/provider/codex_own_test.go`,
  `internal/gateway/codex_own_test.go` and
  `internal/gui/tests/codex-own-prompt-setting.test.cjs`.
