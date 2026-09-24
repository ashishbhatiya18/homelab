(function () {
  'use strict';

  const statusEl = document.getElementById('status');
  const searchBar = document.getElementById('search-bar');
  const searchInput = document.getElementById('search-input');

  const term = new Terminal({
    cursorBlink: true,
    fontFamily: 'Menlo, Consolas, "DejaVu Sans Mono", monospace',
    fontSize: 14,
    scrollback: 10000,
    allowProposedApi: true,
    theme: {
      background: '#0d1117',
      foreground: '#c9d1d9',
      cursor: '#58a6ff',
    },
  });

  const fitAddon = new FitAddon.FitAddon();
  const clipboardAddon = new ClipboardAddon.ClipboardAddon(); // handles OSC 52 (Claude Code's /copy) via navigator.clipboard
  const webLinksAddon = new WebLinksAddon.WebLinksAddon();
  const searchAddon = new SearchAddon.SearchAddon();
  const unicode11Addon = new Unicode11Addon.Unicode11Addon();
  const serializeAddon = new SerializeAddon.SerializeAddon();

  term.loadAddon(fitAddon);
  term.loadAddon(clipboardAddon);
  term.loadAddon(webLinksAddon);
  term.loadAddon(searchAddon);
  term.loadAddon(unicode11Addon);
  term.loadAddon(serializeAddon);
  term.unicode.activeVersion = '11';

  term.open(document.getElementById('terminal'));

  // WebGL rendering is much faster for heavy TUI redraws, but isn't
  // available everywhere (e.g. some mobile browsers) — fall back to the
  // default canvas renderer if it can't initialize.
  try {
    const webglAddon = new WebglAddon.WebglAddon();
    webglAddon.onContextLoss(() => webglAddon.dispose());
    term.loadAddon(webglAddon);
  } catch (err) {
    console.warn('WebGL renderer unavailable, using canvas renderer:', err);
  }

  fitAddon.fit();

  // --- Mouse-selection copy -------------------------------------------------
  // xterm.js keeps text selection purely internal (painted to canvas/WebGL),
  // so there is no real DOM selection for the browser's native copy to act
  // on. This is the exact bug that made copying out of ttyd's terminal not
  // work at all — bridged here directly instead of relying on it.
  function copyWithFallback(text) {
    const ta = document.createElement('textarea');
    ta.value = text;
    ta.setAttribute('readonly', '');
    ta.style.cssText = 'position:fixed;top:0;left:-9999px;';
    document.body.appendChild(ta);
    ta.focus();
    ta.select();
    try {
      document.execCommand('copy');
    } catch (err) {
      // best effort
    }
    document.body.removeChild(ta);
  }

  function copyText(text) {
    if (!text) return;
    if (navigator.clipboard && navigator.clipboard.writeText) {
      navigator.clipboard.writeText(text).catch(() => copyWithFallback(text));
    } else {
      copyWithFallback(text);
    }
  }

  let dragging = false;
  let pendingSelection = null;

  function flushSelectionCopy() {
    const text = pendingSelection;
    pendingSelection = null;
    if (text) copyText(text);
  }

  term.onSelectionChange(() => {
    const sel = term.getSelection();
    if (!sel) return;
    pendingSelection = sel;
    if (!dragging) flushSelectionCopy();
  });

  term.element.addEventListener('mousedown', () => { dragging = true; }, true);
  window.addEventListener('mouseup', () => {
    if (!dragging) return;
    dragging = false;
    flushSelectionCopy();
  }, true);

  // --- Copy visible screen ---------------------------------------------------
  function stripAnsi(str) {
    return str.replace(/\x1b\[[0-9;]*[a-zA-Z]/g, '').replace(/\x1b\][^\x07]*\x07/g, '');
  }

  document.getElementById('copy-screen').addEventListener('click', () => {
    const raw = serializeAddon.serialize({ scrollback: 0 });
    copyText(stripAnsi(raw));
  });

  // --- Search ------------------------------------------------------------
  function openSearch() {
    searchBar.hidden = false;
    searchInput.focus();
  }
  function closeSearch() {
    searchBar.hidden = true;
    searchAddon.clearDecorations();
    term.focus();
  }

  document.getElementById('search-toggle').addEventListener('click', openSearch);
  document.getElementById('search-close').addEventListener('click', closeSearch);
  document.getElementById('search-next').addEventListener('click', () => searchAddon.findNext(searchInput.value));
  document.getElementById('search-prev').addEventListener('click', () => searchAddon.findPrevious(searchInput.value));
  searchInput.addEventListener('keydown', (e) => {
    if (e.key === 'Enter') searchAddon.findNext(searchInput.value);
    if (e.key === 'Escape') closeSearch();
  });
  window.addEventListener('keydown', (e) => {
    const mod = e.ctrlKey || e.metaKey;
    if (mod && e.shiftKey && e.key.toLowerCase() === 'f') {
      e.preventDefault();
      openSearch();
    }
  });

  // --- WebSocket transport -------------------------------------------------
  let ws = null;
  let reconnectDelayMs = 500;
  const RECONNECT_DELAY_MAX_MS = 8000;

  function setStatus(text, cls) {
    statusEl.textContent = text;
    statusEl.className = cls;
  }

  function sendResize() {
    if (ws && ws.readyState === WebSocket.OPEN) {
      ws.send(JSON.stringify({ type: 'resize', cols: term.cols, rows: term.rows }));
    }
  }

  function connect() {
    const proto = location.protocol === 'https:' ? 'wss' : 'ws';
    ws = new WebSocket(`${proto}://${location.host}/ws`);

    ws.addEventListener('open', () => {
      setStatus('connected', 'connected');
      reconnectDelayMs = 500;
      fitAddon.fit();
      sendResize();
    });

    ws.addEventListener('message', (event) => {
      let msg;
      try {
        msg = JSON.parse(event.data);
      } catch {
        return;
      }
      if (msg.type === 'data') term.write(msg.data);
    });

    ws.addEventListener('close', () => {
      setStatus('reconnecting…', 'reconnecting');
      setTimeout(connect, reconnectDelayMs);
      reconnectDelayMs = Math.min(reconnectDelayMs * 2, RECONNECT_DELAY_MAX_MS);
    });

    ws.addEventListener('error', () => {
      setStatus('disconnected', 'disconnected');
      ws.close();
    });
  }

  term.onData((data) => {
    if (ws && ws.readyState === WebSocket.OPEN) {
      ws.send(JSON.stringify({ type: 'data', data }));
    }
  });

  let resizeTimer;
  window.addEventListener('resize', () => {
    clearTimeout(resizeTimer);
    resizeTimer = setTimeout(() => {
      fitAddon.fit();
      sendResize();
    }, 100);
  });
  new ResizeObserver(() => {
    fitAddon.fit();
    sendResize();
  }).observe(document.getElementById('terminal'));

  setStatus('connecting…', 'disconnected');
  connect();
  term.focus();
})();
