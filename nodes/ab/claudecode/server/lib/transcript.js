'use strict';

// Helpers for reading Claude Code's session transcripts
// (~/.claude/projects/<encoded-cwd>/<session-id>.jsonl). Shared by the share
// viewer in server.js and the SessionStart hook, so both agree on where a
// session's transcript lives and what its title is.

const fs = require('fs');
const path = require('path');
const readline = require('readline');

const SESSION_ID_RE = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

// Tool inputs/results can be huge (whole files, long command output); the
// viewer only needs enough to follow along, not a lossless copy.
const MAX_TOOL_INPUT_CHARS = 2000;
const MAX_TOOL_RESULT_CHARS = 4000;
const TITLE_SCAN_BYTES = 256 * 1024;

function truncate(str, max) {
  if (str.length <= max) return str;
  return `${str.slice(0, max)}\n… (${str.length - max} more characters)`;
}

function isSessionId(id) {
  return typeof id === 'string' && SESSION_ID_RE.test(id);
}

// Finds <projectsDir>/*/<sessionId>.jsonl. Session IDs are UUIDs, so a
// match in any project directory is the session.
function findTranscript(projectsDir, sessionId) {
  if (!isSessionId(sessionId)) return null;
  let projects;
  try {
    projects = fs.readdirSync(projectsDir, { withFileTypes: true });
  } catch {
    return null;
  }
  for (const dir of projects) {
    if (!dir.isDirectory()) continue;
    const candidate = path.join(projectsDir, dir.name, `${sessionId}.jsonl`);
    if (fs.existsSync(candidate)) return candidate;
  }
  return null;
}

// Every transcript under projectsDir, as { id, file, mtimeMs }.
function listTranscripts(projectsDir) {
  const out = [];
  let projects;
  try {
    projects = fs.readdirSync(projectsDir, { withFileTypes: true });
  } catch {
    return out;
  }
  for (const dir of projects) {
    if (!dir.isDirectory()) continue;
    const dirPath = path.join(projectsDir, dir.name);
    let files;
    try {
      files = fs.readdirSync(dirPath);
    } catch {
      continue;
    }
    for (const name of files) {
      const id = name.replace(/\.jsonl$/, '');
      if (id === name || !isSessionId(id)) continue;
      try {
        const stat = fs.statSync(path.join(dirPath, name));
        out.push({ id, file: path.join(dirPath, name), mtimeMs: stat.mtimeMs });
      } catch {
        // deleted between readdir and stat
      }
    }
  }
  return out;
}

function userText(content) {
  if (typeof content === 'string') return content;
  if (!Array.isArray(content)) return '';
  return content
    .filter((b) => b && b.type === 'text' && typeof b.text === 'string')
    .map((b) => b.text)
    .join('\n');
}

// Slash commands, their output and system caveats are recorded as user
// messages wrapped in tags like <command-name> / <local-command-stdout>.
function isCommandNoise(text) {
  return /^\s*<(command-|local-command-|system-reminder)/.test(text);
}

// Reads just the head of a transcript for the list view: the first real
// user prompt (as the title), the cwd and the first timestamp.
function summarize(file) {
  const summary = { title: null, cwd: null, startedAt: null };
  let head;
  try {
    const fd = fs.openSync(file, 'r');
    try {
      const buf = Buffer.alloc(TITLE_SCAN_BYTES);
      const n = fs.readSync(fd, buf, 0, buf.length, 0);
      head = buf.subarray(0, n).toString('utf8');
    } finally {
      fs.closeSync(fd);
    }
  } catch {
    return summary;
  }
  for (const line of head.split('\n')) {
    let entry;
    try {
      entry = JSON.parse(line);
    } catch {
      continue; // blank, or the last line cut off by the scan limit
    }
    if (!summary.cwd && entry.cwd) summary.cwd = entry.cwd;
    if (!summary.startedAt && entry.timestamp) summary.startedAt = entry.timestamp;
    if (entry.type !== 'user' || entry.isMeta || entry.isSidechain || !entry.message) continue;
    const text = userText(entry.message.content).trim();
    if (!text || isCommandNoise(text)) continue;
    summary.title = text.split('\n')[0].slice(0, 120);
    break;
  }
  return summary;
}

function stringifyToolInput(input) {
  if (input == null) return '';
  // Show the most telling field inline for common tools instead of raw JSON.
  for (const key of ['command', 'file_path', 'pattern', 'url', 'query', 'description']) {
    if (typeof input[key] === 'string' && Object.keys(input).length <= 3) {
      return truncate(input[key], MAX_TOOL_INPUT_CHARS);
    }
  }
  return truncate(JSON.stringify(input, null, 2), MAX_TOOL_INPUT_CHARS);
}

function toolResultText(content) {
  if (typeof content === 'string') return content;
  if (!Array.isArray(content)) return '';
  return content
    .map((b) => {
      if (b && b.type === 'text') return b.text;
      if (b && b.type === 'image') return '[image]';
      return '';
    })
    .join('\n');
}

// Streams a transcript into a compact list of chat messages:
//   { role: 'user' | 'assistant', ts, blocks: [...] }
// with blocks of { kind: 'text', text }, { kind: 'meta', text },
// { kind: 'tool', name, input } or { kind: 'result', text, isError }.
// Tool results arrive as user-role entries, but they belong to the
// assistant's turn, so they're folded into the preceding assistant message.
async function readChat(file) {
  const messages = [];
  let current = null;

  function push(role, ts, blocks) {
    if (!blocks.length) return;
    if (current && current.role === role) {
      current.blocks.push(...blocks);
      return;
    }
    current = { role, ts, blocks };
    messages.push(current);
  }

  const rl = readline.createInterface({
    input: fs.createReadStream(file, { encoding: 'utf8' }),
    crlfDelay: Infinity,
  });

  for await (const line of rl) {
    if (!line) continue;
    let entry;
    try {
      entry = JSON.parse(line);
    } catch {
      continue;
    }
    if (entry.isSidechain || !entry.message) continue;
    const ts = entry.timestamp || null;
    const content = entry.message.content;

    if (entry.type === 'user') {
      if (entry.isMeta) continue;
      if (typeof content === 'string') {
        const text = content.trim();
        if (text) push('user', ts, [{ kind: isCommandNoise(text) ? 'meta' : 'text', text }]);
        continue;
      }
      if (!Array.isArray(content)) continue;
      const userBlocks = [];
      const resultBlocks = [];
      for (const b of content) {
        if (!b) continue;
        if (b.type === 'tool_result') {
          resultBlocks.push({
            kind: 'result',
            text: truncate(toolResultText(b.content), MAX_TOOL_RESULT_CHARS),
            isError: Boolean(b.is_error),
          });
        } else if (b.type === 'text' && b.text && b.text.trim()) {
          userBlocks.push({ kind: isCommandNoise(b.text) ? 'meta' : 'text', text: b.text });
        } else if (b.type === 'image') {
          userBlocks.push({ kind: 'meta', text: '[image]' });
        }
      }
      push('assistant', ts, resultBlocks);
      push('user', ts, userBlocks);
    } else if (entry.type === 'assistant') {
      if (!Array.isArray(content)) continue;
      const blocks = [];
      for (const b of content) {
        if (!b) continue;
        if (b.type === 'text' && b.text && b.text.trim()) {
          blocks.push({ kind: 'text', text: b.text });
        } else if (b.type === 'tool_use') {
          blocks.push({ kind: 'tool', name: b.name, input: stringifyToolInput(b.input) });
        }
        // thinking blocks are intentionally skipped
      }
      push('assistant', ts, blocks);
    }
  }
  return messages;
}

module.exports = {
  isSessionId,
  findTranscript,
  listTranscripts,
  summarize,
  readChat,
};
