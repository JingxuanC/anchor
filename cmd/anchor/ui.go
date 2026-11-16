package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"time"

	"github.com/JingxuanC/anchor/internal/core"
)

// cmdUI serves a read-only local web view of the Session Tree. It binds to
// loopback only, re-reads the workspace on every request, and never mutates it.
func cmdUI(args []string) {
	fs := flag.NewFlagSet("ui", flag.ExitOnError)
	dir := fs.String("dir", ".", "workspace directory")
	addr := fs.String("addr", "127.0.0.1:0", "loopback listen address (host:port; port 0 picks a free port)")
	_ = fs.Parse(args)
	store := openStore(*dir)

	if err := validateUIAddr(*addr); err != nil {
		die(err)
	}
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		die(fmt.Errorf("cannot listen on %s: %w", *addr, err))
	}
	if !isLoopbackAddr(ln.Addr()) {
		_ = ln.Close()
		die(fmt.Errorf("refusing non-loopback address %s (the UI is local-only)", ln.Addr()))
	}
	url := "http://" + ln.Addr().String() + "/"
	fmt.Printf("anchor ui listening on %s\nworkspace: %s\n(no writes; Ctrl-C to stop)\n", url, store.Dir)

	srv := &http.Server{Handler: uiHandler(store.Dir), ReadHeaderTimeout: 5 * time.Second}
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, wrapSignals()...)
	defer signal.Stop(sig)
	select {
	case <-sig:
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	case err := <-serveErr:
		if err != nil && err != http.ErrServerClosed {
			die(err)
		}
	}
}

// validateUIAddr rejects anything that is not explicitly loopback. Port 0 is
// allowed and resolved by the listener.
func validateUIAddr(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("invalid --addr %q: %w", addr, err)
	}
	if host == "" {
		return fmt.Errorf("--addr must name a loopback host (e.g. 127.0.0.1:0 or [::1]:0)")
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("--addr %q is not a loopback address; the UI is local-only", addr)
	}
	return nil
}

func isLoopbackAddr(addr net.Addr) bool {
	tcp, ok := addr.(*net.TCPAddr)
	return ok && tcp.IP.IsLoopback()
}

// uiHandler serves the static page and the read-only tree API.
func uiHandler(stateDir string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, "ok\n")
	})
	mux.HandleFunc("/api/tree", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "read-only server: mutations are not exposed", http.StatusMethodNotAllowed)
			return
		}
		store, err := core.Open(stateDir)
		if err != nil {
			writeUIError(w, err)
			return
		}
		replay, err := store.LoadReplay()
		if err != nil {
			writeUIError(w, err)
			return
		}
		m, err := readManifest(stateDir)
		if err != nil {
			writeUIError(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(buildTreeView(stateDir, m, replay, false))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "read-only server: mutations are not exposed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = io.WriteString(w, uiPage)
	})
	return mux
}

func writeUIError(w http.ResponseWriter, err error) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusInternalServerError)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

// uiPage is a single self-contained page: no frameworks, no external resources,
// and all data is rendered with textContent (never innerHTML).
const uiPage = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Anchor tree</title>
<style>
:root{color-scheme:light dark}
body{font:14px/1.55 -apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,sans-serif;max-width:1000px;margin:2.5rem auto;padding:0 1rem;background:#fff;color:#111}
@media(prefers-color-scheme:dark){body{background:#111;color:#e8e8e8}.meta,.kv{color:#aaa}details{border-color:#333}.status{background:#223}}
h1{font-size:1.35rem;margin:0 0 .2rem}
.meta{color:#666;font-size:.9rem;margin-bottom:1.2rem}
#status{font-size:.85rem;color:#888;margin-left:.5rem}
#error{color:#b00020;margin:1rem 0;white-space:pre-wrap}
details{margin:.3rem 0 .3rem 1rem;border-left:2px solid #e0e0e0;padding-left:.8rem}
details>summary{cursor:pointer;list-style:none}
.type{font-weight:650;text-transform:uppercase;font-size:.72rem;letter-spacing:.05em;color:#777;margin-right:.4rem}
.id{font-family:ui-monospace,SFMono-Regular,Menlo,monospace;font-size:.85rem;color:#555;margin-right:.4rem}
@media(prefers-color-scheme:dark){.id{color:#bbb}}
.status{font-size:.72rem;border-radius:999px;padding:.06rem .5rem;background:#eef;color:#234;margin-left:.35rem}
.s-done{background:#d1e7dd;color:#0f5132}.s-in_progress{background:#fff3cd;color:#664d03}.s-blocked{background:#f8d7da;color:#842029}.s-abandoned{background:#e2e3e5;color:#41464b}.s-deleted{background:#eee;color:#999}
.kv{margin:.2rem 0 0 1rem;color:#444;font-size:.92rem}
@media(prefers-color-scheme:dark){.kv{color:#ccc}}
footer{margin-top:2.5rem;color:#888;font-size:.8rem}
</style>
</head>
<body>
<h1>Anchor Session Tree</h1>
<div class="meta"><span id="meta">loading…</span><span id="status"></span></div>
<div id="error"></div>
<div id="tree"></div>
<footer>Read-only local view · refreshes every 2s · anchor mcp serve exposes the same data to agents over MCP</footer>
<script>
(function () {
  var tree = document.getElementById('tree');
  var meta = document.getElementById('meta');
  var status = document.getElementById('status');
  var error = document.getElementById('error');

  function el(tag, cls, text) {
    var n = document.createElement(tag);
    if (cls) n.className = cls;
    if (text !== undefined && text !== null) n.textContent = String(text);
    return n;
  }
  function kv(label, value) {
    if (!value) return null;
    var d = el('div', 'kv');
    d.appendChild(el('b', null, label + ': '));
    d.appendChild(document.createTextNode(String(value)));
    return d;
  }
  function badge(n) {
    var s = el('span', 'status s-' + String(n.status || '').replace(/\s+/g, '_'), n.deleted ? 'deleted' : n.status);
    if (n.owner) s.textContent += ' @' + n.owner;
    return s;
  }
  function renderNode(n, children) {
    var details = document.createElement('details');
    details.open = true;
    var summary = el('summary');
    summary.appendChild(el('span', 'type', n.type || ''));
    summary.appendChild(el('span', 'id', n.id || ''));
    summary.appendChild(document.createTextNode(' ' + (n.title || '')));
    summary.appendChild(badge(n));
    details.appendChild(summary);
    [['north star', n.north_star], ['decision', n.decision], ['rationale', n.rationale], ['note', n.note]].forEach(function (p) {
      var d = kv(p[0], p[1]);
      if (d) details.appendChild(d);
    });
    (n.must || []).forEach(function (m) { details.appendChild(kv('must', m)); });
    (n.must_not || []).forEach(function (m) { details.appendChild(kv('must not', m)); });
    (n.success_criteria || []).forEach(function (m) { details.appendChild(kv('success', m)); });
    (n.alternatives || []).forEach(function (m) { details.appendChild(kv('alternative', m)); });
    (n.consequences || []).forEach(function (m) { details.appendChild(kv('consequence', m)); });
    if (n.evidence) details.appendChild(kv('evidence', n.evidence + ' blob(s)'));
    if (n.owner) details.appendChild(kv('owner', n.owner + ' · fencing token ' + (n.fencing_token || 0)));
    (children[n.id] || []).forEach(function (c) { details.appendChild(renderNode(c, children)); });
    return details;
  }
  function render(view) {
    var nodes = view.nodes || [];
    var byId = {};
    nodes.forEach(function (n) { byId[n.id] = true; });
    var children = {};
    var roots = [];
    nodes.forEach(function (n) {
      if (n.parent_id && byId[n.parent_id]) { (children[n.parent_id] = children[n.parent_id] || []).push(n); }
      else roots.push(n);
    });
    var bits = ['workspace ' + (view.workspace_id || '?'), 'device ' + (view.device_id || '?'),
                'projection ' + (view.projection_status || '?'), nodes.length + ' nodes', (view.evidence || []).length + ' evidence'];
    if (view.handoff) bits.push('handoff ' + view.handoff.handoff_id + ' pending=' + view.handoff.pending + ' verified=' + view.handoff.verified);
    meta.textContent = bits.join(' · ');
    tree.replaceChildren();
    if (roots.length === 0) tree.appendChild(el('p', null, 'No nodes yet.'));
    roots.forEach(function (n) { tree.appendChild(renderNode(n, children)); });
    status.textContent = '';
  }
  function refresh() {
    fetch('/api/tree', {cache: 'no-store'}).then(function (r) {
      if (!r.ok) return r.text().then(function (t) { throw new Error(t); });
      return r.json();
    }).then(function (v) { error.textContent = ''; render(v); })
      .catch(function (e) { error.textContent = 'cannot load the tree: ' + e.message; });
  }
  refresh();
  setInterval(refresh, 2000);
})();
</script>
</body>
</html>
`
