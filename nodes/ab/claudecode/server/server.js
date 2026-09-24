'use strict';

const http = require('http');
const fs = require('fs');
const path = require('path');
const crypto = require('crypto');
const WebSocket = require('ws');
const pty = require('node-pty');

const PORT = Number(process.env.PORT || 7681);
const PUBLIC_DIR = path.join(__dirname, 'public');
const TERMINAL_CMD = process.env.TERMINAL_CMD || 'claude';
const TERMINAL_ARGS = process.env.TERMINAL_ARGS ? process.env.TERMINAL_ARGS.split(' ') : [];
const TERMINAL_CWD = process.env.TERMINAL_CWD || process.env.HOME || '/workspace';
// Access is already gated upstream by Traefik's passkey (webauthn) forward-auth
// middleware, but these are cheap defense-in-depth caps against a client that
// makes it past that gate (or a bug in it): a runaway input flood can't grow
// memory unbounded, and a connection loop can't fork unlimited PTYs/processes.
const MAX_WS_PAYLOAD_BYTES = Number(process.env.MAX_WS_PAYLOAD_BYTES || 64 * 1024);
const MAX_CONCURRENT_SESSIONS = Number(process.env.MAX_CONCURRENT_SESSIONS || 8);

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

const server = http.createServer(serveStatic);
const wss = new WebSocket.Server({ server, path: '/ws', maxPayload: MAX_WS_PAYLOAD_BYTES });

let activeSessions = 0;

wss.on('connection', (ws) => {
  if (activeSessions >= MAX_CONCURRENT_SESSIONS) {
    console.warn(`rejecting connection: ${MAX_CONCURRENT_SESSIONS} sessions already active`);
    ws.close(1013, 'too many active sessions');
    return;
  }
  activeSessions += 1;

  const sessionId = crypto.randomUUID();
  const term = pty.spawn(TERMINAL_CMD, TERMINAL_ARGS, {
    name: 'xterm-256color',
    cols: 80,
    rows: 24,
    cwd: TERMINAL_CWD,
    env: process.env,
  });

  console.log(`[${sessionId}] session started, pid=${term.pid}`);

  term.onData((data) => {
    if (ws.readyState === WebSocket.OPEN) {
      ws.send(JSON.stringify({ type: 'data', data }));
    }
  });

  term.onExit(({ exitCode }) => {
    console.log(`[${sessionId}] terminal process exited (${exitCode})`);
    if (ws.readyState === WebSocket.OPEN) ws.close();
  });

  ws.on('message', (raw) => {
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
    console.log(`[${sessionId}] websocket closed, killing pty`);
    activeSessions -= 1;
    term.kill();
  });

  ws.on('error', () => {
    term.kill();
  });
});

server.listen(PORT, () => {
  console.log(`claudecode terminal server listening on :${PORT}`);
});
