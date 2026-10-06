'use strict';

// Claude Code SessionStart hook (registered in managed/managed-settings.json).
// Runs before the first prompt — and again on resume, /clear and compaction —
// to give the session its share space: creates $SHARE_DIR/s/<session-id>/
// with a manifest.json, then injects the directory and its viewer URL into
// the session's context so Claude can publish documents there.
//
// It must never block a session from starting, so every failure is logged
// to stderr and the hook still exits 0.

const fs = require('fs');
const path = require('path');
const { isSessionId } = require('../lib/transcript');

const SHARE_DIR = process.env.SHARE_DIR || '/share';
const SHARE_BASE_URL = (process.env.SHARE_BASE_URL || 'https://term.ab18.in/share').replace(/\/+$/, '');

function readStdin() {
  try {
    return JSON.parse(fs.readFileSync(0, 'utf8'));
  } catch {
    return {};
  }
}

function main() {
  const input = readStdin();
  const id = input.session_id;
  if (!isSessionId(id)) {
    console.error(`share hook: unexpected session_id ${JSON.stringify(id)}, skipping`);
    return;
  }

  const dir = path.join(SHARE_DIR, 's', id);
  const url = `${SHARE_BASE_URL}/s/${id}`;
  const manifestPath = path.join(dir, 'manifest.json');
  const now = new Date().toISOString();

  fs.mkdirSync(dir, { recursive: true });
  let manifest;
  try {
    manifest = JSON.parse(fs.readFileSync(manifestPath, 'utf8'));
  } catch {
    manifest = { session_id: id, cwd: input.cwd || null, url, started_at: now };
  }
  manifest.last_event = { source: input.source || 'startup', at: now };
  fs.writeFileSync(manifestPath, `${JSON.stringify(manifest, null, 2)}\n`);

  const context = [
    'Share space for this session:',
    `- Directory: ${dir}/`,
    `- Viewer URL: ${url}`,
    `- Link to a single document: ${url}#<file-name>`,
    'Follow the "Share space" rules in the managed CLAUDE.md for when and how to publish there.',
  ].join('\n');

  process.stdout.write(JSON.stringify({
    hookSpecificOutput: { hookEventName: 'SessionStart', additionalContext: context },
  }));
}

try {
  main();
} catch (err) {
  console.error(`share hook: ${err && err.message}`);
}
