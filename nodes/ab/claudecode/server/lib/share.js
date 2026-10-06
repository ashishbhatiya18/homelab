'use strict';

// Read-only companion viewer mounted at /share on the terminal server:
// lists Claude Code sessions, renders their chat history from the
// transcript JSONL, and shows the documents Claude published to
// $SHARE_DIR/s/<session-id>/ (see hooks/session-start.js).
//
// This runs on the same origin as /ws, which spawns a shell — so anything
// Claude wrote (HTML, SVG) is only ever served from /share/raw with
// "CSP: sandbox", giving it an opaque origin that /ws's Origin check
// rejects. The viewer's own pages are locked down with script-src 'self'.

const fs = require('fs');
const path = require('path');
const transcript = require('./transcript');

const SHARE_DIR = process.env.SHARE_DIR || '/share';
const PROJECTS_DIR =
  process.env.CLAUDE_PROJECTS_DIR ||
  path.join(process.env.CLAUDE_CONFIG_DIR || path.join(process.env.HOME || '', '.claude'), 'projects');
const SHELL_PAGE = path.join(__dirname, '..', 'public', 'share', 'index.html');
const MAX_SESSIONS = Number(process.env.SHARE_MAX_SESSIONS || 20);

const BASE_HEADERS = {
  'X-Content-Type-Options': 'nosniff',
  'Referrer-Policy': 'no-referrer',
  'Cache-Control': 'no-store',
};

const VIEWER_CSP = [
  "default-src 'none'",
  "script-src 'self'",
  "style-src 'self'",
  "img-src 'self' data:",
  "connect-src 'self'",
  "frame-src 'self'",
  "base-uri 'none'",
  "form-action 'none'",
  "frame-ancestors 'none'",
].join('; ');

// Published files get an opaque origin (no cookies, no same-origin access
// to the viewer or /ws) but may still run their own scripts, so an HTML
// report with a chart works. Only the viewer itself may frame them.
const RAW_CSP = "sandbox allow-scripts; frame-ancestors 'self'";

const DOC_TYPES = {
  markdown: ['.md', '.markdown'],
  html: ['.html', '.htm'],
  image: ['.png', '.jpg', '.jpeg', '.gif', '.webp', '.svg'],
  pdf: ['.pdf'],
  code: [
    '.js', '.mjs', '.ts', '.tsx', '.jsx', '.json', '.py', '.go', '.rs', '.sh', '.bash',
    '.yaml', '.yml', '.toml', '.ini', '.sql', '.css', '.java', '.c', '.h', '.cpp',
    '.rb', '.php', '.tf', '.hcl', '.xml', '.diff', '.patch', '.dockerfile',
  ],
  text: ['.txt', '.log', '.csv', '.tsv', '.env.example'],
};

const RAW_CONTENT_TYPES = {
  '.html': 'text/html; charset=utf-8',
  '.htm': 'text/html; charset=utf-8',
  '.svg': 'image/svg+xml',
  '.png': 'image/png',
  '.jpg': 'image/jpeg',
  '.jpeg': 'image/jpeg',
  '.gif': 'image/gif',
  '.webp': 'image/webp',
  '.pdf': 'application/pdf',
};

const FILE_NAME_RE = /^[A-Za-z0-9_][A-Za-z0-9._ -]{0,199}$/;

function docType(name) {
  const ext = path.extname(name).toLowerCase();
  for (const [type, exts] of Object.entries(DOC_TYPES)) {
    if (exts.includes(ext)) return type;
  }
  return 'other';
}

function send(res, status, headers, body) {
  res.writeHead(status, { ...BASE_HEADERS, ...headers });
  res.end(body);
}

function sendJson(res, status, obj) {
  send(res, status, { 'Content-Type': 'application/json; charset=utf-8' }, JSON.stringify(obj));
}

function sessionDir(id) {
  return path.join(SHARE_DIR, 's', id);
}

// Resolves a published file, refusing anything that isn't a plain file
// sitting directly in the session's share directory — in particular a
// symlink planted there pointing at e.g. ~/.claude/.credentials.json.
function resolveDoc(id, name) {
  if (!transcript.isSessionId(id) || !FILE_NAME_RE.test(name) || name === 'manifest.json') return null;
  const expected = path.join(sessionDir(id), name);
  try {
    const shareRoot = fs.realpathSync(SHARE_DIR);
    const real = fs.realpathSync(expected);
    if (real !== path.join(shareRoot, 's', id, name)) return null;
    const stat = fs.lstatSync(real);
    return stat.isFile() ? { file: real, stat } : null;
  } catch {
    return null;
  }
}

function readManifest(id) {
  try {
    return JSON.parse(fs.readFileSync(path.join(sessionDir(id), 'manifest.json'), 'utf8'));
  } catch {
    return null;
  }
}

function docTitle(file, name, type) {
  if (type !== 'markdown' && type !== 'html') return name;
  let head = '';
  try {
    const fd = fs.openSync(file, 'r');
    try {
      const buf = Buffer.alloc(4096);
      head = buf.subarray(0, fs.readSync(fd, buf, 0, buf.length, 0)).toString('utf8');
    } finally {
      fs.closeSync(fd);
    }
  } catch {
    return name;
  }
  const match =
    type === 'markdown' ? head.match(/^#\s+(.+)$/m) : head.match(/<title[^>]*>([^<]+)<\/title>/i);
  return match ? match[1].trim().slice(0, 160) : name;
}

function listDocs(id) {
  let names;
  try {
    names = fs.readdirSync(sessionDir(id));
  } catch {
    return [];
  }
  const docs = [];
  for (const name of names) {
    const doc = resolveDoc(id, name);
    if (!doc) continue;
    const type = docType(name);
    docs.push({
      file: name,
      title: docTitle(doc.file, name, type),
      type,
      size: doc.stat.size,
      mtime: new Date(doc.stat.mtimeMs).toISOString(),
    });
  }
  return docs.sort((a, b) => b.mtime.localeCompare(a.mtime));
}

// A session's first prompt never changes once written, so a found title is
// cached for good; sessions without one yet are re-scanned on each request.
const summaryCache = new Map();

function transcriptSummary(file) {
  const cached = summaryCache.get(file);
  if (cached && cached.title) return cached;
  const summary = transcript.summarize(file);
  summaryCache.set(file, summary);
  return summary;
}

function sessionInfo(id, transcriptEntry) {
  const manifest = readManifest(id);
  const docs = listDocs(id);
  const summary = transcriptEntry ? transcriptSummary(transcriptEntry.file) : {};
  const times = [
    transcriptEntry ? transcriptEntry.mtimeMs : 0,
    ...docs.map((d) => Date.parse(d.mtime)),
    manifest && manifest.started_at ? Date.parse(manifest.started_at) : 0,
  ];
  return {
    id,
    title: summary.title || (docs[0] && docs[0].title) || 'Untitled session',
    cwd: summary.cwd || (manifest && manifest.cwd) || null,
    startedAt: summary.startedAt || (manifest && manifest.started_at) || null,
    updatedAt: new Date(Math.max(...times.filter(Number.isFinite))).toISOString(),
    hasTranscript: Boolean(transcriptEntry),
    docs,
  };
}

// Only the most recent MAX_SESSIONS are listed. Candidates are picked by a
// cheap recency key (transcript mtime, or the share directory's mtime, which
// changes whenever a document is added) so only those few transcripts get
// their heads read, not every session on disk.
function listSessions() {
  const candidates = new Map();
  for (const t of transcript.listTranscripts(PROJECTS_DIR)) {
    candidates.set(t.id, { entry: t, recency: t.mtimeMs });
  }
  let shared = [];
  try {
    shared = fs.readdirSync(path.join(SHARE_DIR, 's')).filter(transcript.isSessionId);
  } catch {
    // no sessions published yet
  }
  for (const id of shared) {
    let dirMtime = 0;
    try {
      dirMtime = fs.statSync(sessionDir(id)).mtimeMs;
    } catch {
      continue;
    }
    const c = candidates.get(id);
    if (c) c.recency = Math.max(c.recency, dirMtime);
    else candidates.set(id, { entry: null, recency: dirMtime });
  }

  return [...candidates.entries()]
    .sort((a, b) => b[1].recency - a[1].recency)
    .slice(0, MAX_SESSIONS)
    .map(([id, c]) => {
      const info = sessionInfo(id, c.entry);
      return { ...info, docCount: info.docs.length, docs: undefined };
    })
    .sort((a, b) => b.updatedAt.localeCompare(a.updatedAt));
}

function transcriptEntryFor(id) {
  const file = transcript.findTranscript(PROJECTS_DIR, id);
  if (!file) return null;
  try {
    return { id, file, mtimeMs: fs.statSync(file).mtimeMs };
  } catch {
    return null;
  }
}

function serveShell(res) {
  fs.readFile(SHELL_PAGE, (err, data) => {
    if (err) return send(res, 500, {}, 'share viewer missing');
    send(res, 200, {
      'Content-Type': 'text/html; charset=utf-8',
      'Content-Security-Policy': VIEWER_CSP,
      'X-Frame-Options': 'DENY',
    }, data);
  });
}

function serveRaw(res, id, name, method) {
  const doc = resolveDoc(id, name);
  if (!doc) return send(res, 404, { 'Content-Type': 'text/plain' }, 'not found');
  const ext = path.extname(name).toLowerCase();
  const contentType = RAW_CONTENT_TYPES[ext] || 'text/plain; charset=utf-8';
  res.writeHead(200, {
    ...BASE_HEADERS,
    'Content-Type': contentType,
    'Content-Length': doc.stat.size,
    'Content-Security-Policy': RAW_CSP,
  });
  if (method === 'HEAD') return res.end();
  fs.createReadStream(doc.file).pipe(res);
}

function apiSession(res, id) {
  const entry = transcript.isSessionId(id) ? transcriptEntryFor(id) : null;
  if (!entry && !(transcript.isSessionId(id) && fs.existsSync(sessionDir(id)))) {
    sendJson(res, 404, { error: 'not found' });
    return;
  }
  sendJson(res, 200, sessionInfo(id, entry));
}

function apiChat(res, id) {
  const entry = transcript.isSessionId(id) ? transcriptEntryFor(id) : null;
  if (!entry) {
    sendJson(res, 404, { error: 'no transcript' });
    return;
  }
  transcript
    .readChat(entry.file)
    .then((messages) => sendJson(res, 200, { messages }))
    .catch((err) => {
      console.error(`share: failed to read transcript ${entry.file}:`, err);
      sendJson(res, 500, { error: 'failed to read transcript' });
    });
}

// Returns true if the request was a /share request (and has been handled).
function handle(req, res) {
  const url = new URL(req.url, 'http://localhost');
  const p = url.pathname;
  if (p !== '/share' && !p.startsWith('/share/')) return false;

  if (req.method !== 'GET' && req.method !== 'HEAD') {
    send(res, 405, {}, 'method not allowed');
    return true;
  }

  let m;
  if (p === '/share' || p === '/share/' || /^\/share\/s\/[^/]+\/?$/.test(p)) {
    serveShell(res);
  } else if (p === '/share/latest') {
    const [latest] = listSessions();
    send(res, 302, { Location: latest ? `/share/s/${latest.id}` : '/share' }, '');
  } else if (p === '/share/api/sessions') {
    sendJson(res, 200, { sessions: listSessions() });
  } else if ((m = p.match(/^\/share\/api\/s\/([^/]+)$/))) {
    apiSession(res, m[1]);
  } else if ((m = p.match(/^\/share\/api\/s\/([^/]+)\/chat$/))) {
    apiChat(res, m[1]);
  } else if ((m = p.match(/^\/share\/raw\/([^/]+)\/([^/]+)$/))) {
    let name;
    try {
      name = decodeURIComponent(m[2]);
    } catch {
      name = '';
    }
    serveRaw(res, m[1], name, req.method);
  } else {
    // /share/share.js, /share/share.css → normal static files
    return false;
  }
  return true;
}

module.exports = { handle };
