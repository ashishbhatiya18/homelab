'use strict';

const http = require('http');
const fs = require('fs');
const path = require('path');
const WebSocket = require('ws');
const pty = require('node-pty');
const share = require('./lib/share');

const PORT = Number(process.env.PORT || 7681);
const PUBLIC_DIR = path.join(__dirname, 'public');
const TERMINAL_CMD = process.env.TERMINAL_CMD || 'claude';
const TERMINAL_ARGS = process.env.TERMINAL_ARGS ? process.env.TERMINAL_ARGS.split(' ') : [];
const TERMINAL_CWD = process.env.TERMINAL_CWD || process.env.HOME || '/workspace';
// Access is already gated upstream by Traefik's passkey (Authelia) forward-auth
// middleware, but these are cheap defense-in-depth caps against a client that
// makes it past that gate (or a bug in it): a runaway input flood can't grow
// memory unbounded, and a connection loop can't fork unlimited PTYs/processes.
const MAX_WS_PAYLOAD_BYTES = Number(process.env.MAX_WS_PAYLOAD_BYTES || 64 * 1024);
const MAX_CONCURRENT_SESSIONS = Number(process.env.MAX_CONCURRENT_SESSIONS || 8);
// Origins allowed to open /ws. Defaults to the page's own host, which is all
// the terminal UI needs. Documents served from /share/raw run in a CSP
// sandbox and send "Origin: null", so this is also what stops a published
// HTML page from driving the shell.
const ALLOWED_ORIGINS = (process.env.ALLOWED_ORIGINS || '').split(',').map((s) => s.trim()).filter(Boolean);

const MIME_TYPES = {
  '.html': 'text/html; charset=utf-8',
  '.js': 'application/javascript; charset=utf-8',
  '.css': 'text/css; charset=utf-8',
  '.map': 'application/json; charset=utf-8',
};

const SECURITY_HEADERS = {
  'X-Content-Type-Options': 'nosniff',
  'X-Frame-Options': 'DENY',
  'Referrer-Policy': 'no-referrer',
};

function serveStatic(req, res) {
  if (req.method !== 'GET' && req.method !== 'HEAD') {
    res.writeHead(405, SECURITY_HEADERS);
    res.end('method not allowed');
    return;
  }

  const reqPath = (req.url === '/' ? '/index.html' : req.url).split('?')[0];
  const filePath = path.normalize(path.join(PUBLIC_DIR, reqPath));

  // Reject anything that would escape PUBLIC_DIR (e.g. "../../etc/passwd").
  if (!filePath.startsWith(PUBLIC_DIR + path.sep) && filePath !== PUBLIC_DIR) {
    res.writeHead(403, SECURITY_HEADERS);
    res.end('forbidden');
    return;
  }

  fs.readFile(filePath, (err, data) => {
    if (err) {
      res.writeHead(404, SECURITY_HEADERS);
      res.end('not found');
      return;
    }
    res.writeHead(200, {
      ...SECURITY_HEADERS,
      'Content-Type': MIME_TYPES[path.extname(filePath)] || 'application/octet-stream',
      // Without this, browsers' default heuristic caching can keep serving a
      // stale app.js/index.html after a redeploy until a hard refresh — this
      // app has no versioned asset filenames to bust that cache, and we send
      // no ETag/Last-Modified for "no-cache" to revalidate against, so
      // "no-store" (never cache at all) is the only directive that reliably
      // guarantees freshness here.
      'Cache-Control': 'no-store',
    });
    res.end(data);
  });
}

function isAllowedOrigin({ origin, req }) {
  if (ALLOWED_ORIGINS.length) return ALLOWED_ORIGINS.includes(origin);
  const host = req.headers.host;
  return Boolean(host) && (origin === `https://${host}` || origin === `http://${host}`);
}

const server = http.createServer((req, res) => {
  if (!share.handle(req, res)) serveStatic(req, res);
});
const wss = new WebSocket.Server({
  server,
  path: '/ws',
  maxPayload: MAX_WS_PAYLOAD_BYTES,
  verifyClient: (info) => {
    const ok = isAllowedOrigin(info);
    if (!ok) console.warn(`rejecting websocket from origin ${JSON.stringify(info.origin)}`);
    return ok;
  },
});

// One shared terminal for every connection: closing a tab (or a dropped
// websocket) detaches from it instead of killing it, so the Claude session
// survives reconnects and every open tab shows the same session.
const SCROLLBACK_BYTES = 2 * 1024 * 1024;
const PING_INTERVAL_MS = 30 * 1000;
const clients = new Set();
let term = null;
let scrollback = '';

function broadcast(msg) {
  const payload = JSON.stringify(msg);
  for (const ws of clients) {
    if (ws.readyState === WebSocket.OPEN) ws.send(payload);
  }
}

function ensureTerm() {
  if (term) return term;
  term = pty.spawn(TERMINAL_CMD, TERMINAL_ARGS, {
    name: 'xterm-256color',
    cols: 80,
    rows: 24,
    cwd: TERMINAL_CWD,
    env: process.env,
  });
  console.log(`terminal started, pid=${term.pid}`);

  term.onData((data) => {
    // shortcut: raw byte replay, trimmed from the front, can cut an escape
    // sequence in half on a new tab; use @xterm/headless + serialize if it shows.
    scrollback = (scrollback + data).slice(-SCROLLBACK_BYTES);
    broadcast({ type: 'data', data });
  });

  term.onExit(({ exitCode }) => {
    console.log(`terminal process exited (${exitCode})`);
    term = null;
    scrollback = '';
    // Clients reconnect on close and get a fresh terminal.
    for (const ws of clients) ws.close();
  });
  return term;
}

// Cloudflare's tunnel drops websockets idle for ~100s; pings keep them open
// and reap connections whose peer went away without closing.
setInterval(() => {
  for (const ws of clients) {
    if (ws.isAlive === false) {
      ws.terminate();
      continue;
    }
    ws.isAlive = false;
    ws.ping();
  }
}, PING_INTERVAL_MS);

wss.on('connection', (ws) => {
  if (clients.size >= MAX_CONCURRENT_SESSIONS) {
    console.warn(`rejecting connection: ${MAX_CONCURRENT_SESSIONS} connections already open`);
    ws.close(1013, 'too many connections');
    return;
  }
  clients.add(ws);
  ws.isAlive = true;
  ws.on('pong', () => {
    ws.isAlive = true;
  });

  ensureTerm();
  if (scrollback) ws.send(JSON.stringify({ type: 'data', data: scrollback }));

  ws.on('message', (raw) => {
    if (!term) return;
    let msg;
    try {
      msg = JSON.parse(raw.toString());
    } catch {
      return;
    }
    if (msg.type === 'data' && typeof msg.data === 'string') {
      term.write(msg.data);
    } else if (msg.type === 'resize') {
      const cols = Number(msg.cols);
      const rows = Number(msg.rows);
      const inBounds = (n) => Number.isInteger(n) && n > 0 && n <= 1000;
      if (inBounds(cols) && inBounds(rows)) {
        term.resize(cols, rows);
      }
    }
  });

  ws.on('close', () => {
    clients.delete(ws);
  });

  ws.on('error', () => {
    ws.terminate();
  });
});

server.listen(PORT, () => {
  console.log(`claudecode terminal server listening on :${PORT}`);
});
