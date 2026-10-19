package gateway

import (
	"net/http"

	"github.com/yetone/magpie/internal/provider"
)

// codexCalled has every request carry how a ChatGPT account's request made
// for it goes (provider.WithCodexCall, own/codex-own-prompt.md): whether
// Codex sent it, judged by the headers it came with (fromCodex), and
// Codex's own name to relay when it did.
func codexCalled(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(provider.WithCodexCall(r.Context(), r.Header, fromCodex(r.Header))))
	})
}
