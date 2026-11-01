package main

import (
	"encoding/json"
	"html"
	"net/http"
	"strings"
)

// launchStatus is what the window says until the Web UI loads, and when the
// app cannot go on.
type launchStatus struct {
	Title  string `json:"title"`
	Detail string `json:"detail"`
	Failed bool   `json:"failed"`
	// Waiting is a launch waiting for the user, so nothing spins.
	Waiting bool `json:"waiting"`
}

// ServeHTTP serves the status page inside the app: the page, and /status,
// which the page polls so it follows the launch without being reloaded.
func (s *shell) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	status := s.launch()
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Path == "/status" {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(status)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(statusPage(status)))
}

// statusPage follows the Web UI's look: its colors, in dark and light.
func statusPage(status launchStatus) string {
	return strings.NewReplacer(
		"{{title}}", html.EscapeString(status.Title),
		"{{detail}}", html.EscapeString(status.Detail),
		"{{state}}", status.state(),
	).Replace(statusTemplate)
}

// state is the page's class: how the spinner looks.
func (status launchStatus) state() string {
	switch {
	case status.Failed:
		return "failed"
	case status.Waiting:
		return "waiting"
	default:
		return ""
	}
}

const statusTemplate = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>Mini-Orca</title>
<style>
  :root { color-scheme: dark; --chrome: #0a0b0d; --text: #ececef; --muted: #8a8f99; --accent: #7f86ff; --danger: #f07178; --track: rgba(255, 255, 255, 0.08); }
  @media (prefers-color-scheme: light) {
    :root { color-scheme: light; --chrome: #efeff2; --text: #16171b; --muted: #676b75; --accent: #6d74f7; --danger: #d14343; --track: rgba(20, 22, 30, 0.09); }
  }
  html, body { height: 100%; margin: 0; }
  body {
    display: grid; place-items: center; background: var(--chrome); color: var(--text);
    font: 14px/1.5 ui-sans-serif, system-ui, -apple-system, 'Segoe UI', sans-serif;
    -webkit-user-select: none; user-select: none; cursor: default;
  }
  main { display: grid; justify-items: center; gap: 14px; max-width: 520px; padding: 0 24px; text-align: center; }
  .spinner { width: 22px; height: 22px; border-radius: 50%; border: 2px solid var(--track); border-top-color: var(--accent); animation: spin 0.8s linear infinite; }
  .failed .spinner { animation: none; border-color: var(--danger); }
  .waiting .spinner { visibility: hidden; }
  h1 { margin: 0; font-size: 15px; font-weight: 600; }
  p { margin: 0; color: var(--muted); font-size: 13px; word-break: break-word; -webkit-user-select: text; user-select: text; }
  @keyframes spin { to { transform: rotate(360deg); } }
  @media (prefers-reduced-motion: reduce) { .spinner { animation-duration: 2.4s; } }
</style>
</head>
<body class="{{state}}">
<main role="status" aria-live="polite">
  <div class="spinner" aria-hidden="true"></div>
  <h1 id="title">{{title}}</h1>
  <p id="detail">{{detail}}</p>
</main>
<script>
  const title = document.getElementById('title');
  const detail = document.getElementById('detail');
  async function follow() {
    try {
      const status = await (await fetch('/status', { cache: 'no-store' })).json();
      title.textContent = status.title;
      detail.textContent = status.detail;
      document.body.className = status.failed ? 'failed' : status.waiting ? 'waiting' : '';
    } catch {}
    setTimeout(follow, 250);
  }
  setTimeout(follow, 250);
</script>
</body>
</html>`

// navigationKeys gives the window a browser's back and forward: Cmd+[ and
// Cmd+] on macOS, Alt+Left and Alt+Right elsewhere, and the mouse's side
// buttons. Typing in a field keeps its own keys.
const navigationKeys = `(() => {
  if (window.__miniOrcaNavigation) return;
  window.__miniOrcaNavigation = true;
  const mac = /Mac/.test(navigator.platform);
  const editable = el => el && (el.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(el.tagName));
  addEventListener('keydown', event => {
    if (editable(event.target)) return;
    const back = mac ? event.metaKey && event.key === '[' : event.altKey && event.key === 'ArrowLeft';
    const forward = mac ? event.metaKey && event.key === ']' : event.altKey && event.key === 'ArrowRight';
    if (!back && !forward) return;
    event.preventDefault();
    back ? history.back() : history.forward();
  });
  addEventListener('mouseup', event => {
    if (event.button !== 3 && event.button !== 4) return;
    event.preventDefault();
    event.button === 3 ? history.back() : history.forward();
  });
})();`
