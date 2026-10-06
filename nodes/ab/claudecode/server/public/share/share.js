(function () {
  'use strict';

  const app = document.getElementById('app');
  const crumb = document.getElementById('crumb');
  const back = document.getElementById('back');
  const POLL_MS = 5000;

  hljs.configure({ ignoreUnescapedHTML: true });

  // --- helpers ---------------------------------------------------------------
  function h(tag, attrs, ...children) {
    const el = document.createElement(tag);
    for (const [k, v] of Object.entries(attrs || {})) {
      if (v == null || v === false) continue;
      if (k === 'class') el.className = v;
      else el.setAttribute(k, v === true ? '' : v);
    }
    for (const c of children.flat()) {
      if (c == null || c === false) continue;
      el.append(c instanceof Node ? c : String(c));
    }
    return el;
  }

  async function getJson(url) {
    const res = await fetch(url, { cache: 'no-store' });
    if (!res.ok) throw new Error(`${res.status} ${url}`);
    return res.json();
  }

  function relTime(iso) {
    if (!iso) return '';
    const s = Math.round((Date.now() - Date.parse(iso)) / 1000);
    if (s < 60) return 'just now';
    if (s < 3600) return `${Math.floor(s / 60)}m ago`;
    if (s < 86400) return `${Math.floor(s / 3600)}h ago`;
    if (s < 86400 * 7) return `${Math.floor(s / 86400)}d ago`;
    return new Date(iso).toLocaleDateString();
  }

  function fmtSize(n) {
    if (n < 1024) return `${n} B`;
    if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
    return `${(n / 1024 / 1024).toFixed(1)} MB`;
  }

  function rawUrl(sessionId, file) {
    return `/share/raw/${sessionId}/${encodeURIComponent(file)}`;
  }

  // Markdown → sanitized DOM. Relative image/link targets point at sibling
  // files in the same share directory, so they're rewritten to raw URLs
  // (images) or to that document's in-viewer anchor (links).
  function renderMarkdown(text, sessionId) {
    const wrap = h('div', { class: 'md' });
    wrap.innerHTML = DOMPurify.sanitize(marked.parse(text, { gfm: true }), {
      FORBID_TAGS: ['style', 'form', 'input'],
      FORBID_ATTR: ['style'],
    });
    const isRelative = (u) => u && !/^([a-z][a-z0-9+.-]*:|\/\/|#|\/)/i.test(u);
    wrap.querySelectorAll('img[src]').forEach((img) => {
      const src = img.getAttribute('src');
      if (sessionId && isRelative(src)) img.src = rawUrl(sessionId, src.replace(/^\.\//, ''));
    });
    wrap.querySelectorAll('a[href]').forEach((a) => {
      const href = a.getAttribute('href');
      if (sessionId && isRelative(href)) {
        a.setAttribute('href', `#${encodeURIComponent(href.replace(/^\.\//, ''))}`);
      } else if (/^https?:/i.test(href)) {
        a.target = '_blank';
        a.rel = 'noopener noreferrer';
      }
    });
    wrap.querySelectorAll('pre code').forEach((el) => hljs.highlightElement(el));
    return wrap;
  }

  function codeLanguage(file) {
    const ext = file.split('.').pop().toLowerCase();
    const map = { yml: 'yaml', tf: 'hcl', sh: 'bash', mjs: 'javascript', jsx: 'javascript', tsx: 'typescript' };
    const lang = map[ext] || ext;
    return hljs.getLanguage(lang) ? lang : null;
  }

  // --- session list ------------------------------------------------------------
  async function showList() {
    crumb.textContent = '';
    back.hidden = true;
    document.title = 'Claude Share';
    const { sessions } = await getJson('/share/api/sessions');
    app.replaceChildren(
      h('section', { class: 'sessions' },
        h('h1', null, 'Sessions'),
        sessions.length
          ? sessions.map((s) =>
            h('a', { class: 'session-row', href: `/share/s/${s.id}` },
              h('div', { class: 'session-title' }, s.title),
              h('div', { class: 'session-meta' },
                h('span', null, relTime(s.updatedAt)),
                s.cwd && h('span', null, s.cwd),
                s.docCount > 0 && h('span', { class: 'badge docs' }, `${s.docCount} doc${s.docCount > 1 ? 's' : ''}`),
                !s.hasTranscript && h('span', { class: 'badge' }, 'no transcript'))))
          : h('p', { class: 'empty' }, 'No sessions yet. Start Claude in the terminal.'),
        sessions.length > 0 && h('p', { class: 'note' }, `Showing the ${sessions.length} most recent sessions.`)));
  }

  // --- session view ----------------------------------------------------------
  let session = null;
  let renderedKey = null;

  function currentTarget() {
    let hash = '';
    try {
      hash = decodeURIComponent(location.hash.slice(1));
    } catch {
      // malformed escape in a hand-edited URL; fall back to the default view
    }
    if (hash && session.docs.some((d) => d.file === hash)) return hash;
    if (!hash && !session.hasTranscript && session.docs.length) return session.docs[0].file;
    return 'chat';
  }

  function renderSidebar(active) {
    return h('nav', { class: 'sidebar' },
      h('div', { class: 'session-info' }, session.cwd || '', session.startedAt ? ` · ${new Date(session.startedAt).toLocaleString()}` : ''),
      h('h2', null, 'Session'),
      session.hasTranscript && h('a', { class: `nav-item${active === 'chat' ? ' active' : ''}`, href: '#chat' }, '💬 Chat history'),
      h('h2', null, `Documents (${session.docs.length})`),
      session.docs.length
        ? session.docs.map((d) =>
          h('a', { class: `nav-item${active === d.file ? ' active' : ''}`, href: `#${encodeURIComponent(d.file)}`, title: d.file },
            d.title,
            h('small', null, `${d.file} · ${relTime(d.mtime)}`)))
        : h('div', { class: 'session-info' }, 'Nothing published yet.'));
  }

  function renderChatBlock(block) {
    if (block.kind === 'text') return renderMarkdown(block.text, session.id);
    if (block.kind === 'meta') return h('div', { class: 'meta-line' }, block.text);
    if (block.kind === 'tool') {
      const firstLine = (block.input || '').split('\n')[0];
      return h('details', { class: 'tool' },
        h('summary', null, h('span', { class: 'tool-name' }, block.name), firstLine),
        h('pre', null, block.input));
    }
    if (block.kind === 'result') {
      const firstLine = (block.text || '').trim().split('\n')[0] || '(no output)';
      return h('details', { class: `tool${block.isError ? ' error' : ''}` },
        h('summary', null, h('span', { class: 'tool-name' }, block.isError ? '✗ error' : '↳ output'), firstLine),
        h('pre', null, block.text));
    }
    return null;
  }

  async function renderChat(pane) {
    const { messages } = await getJson(`/share/api/s/${session.id}/chat`);
    pane.append(
      h('div', { class: 'pane-head' }, h('h1', null, 'Chat history')),
      messages.length
        ? h('div', { class: 'chat' }, messages.map((m) =>
          h('div', { class: `msg ${m.role}` },
            h('div', { class: 'msg-head' },
              h('strong', null, m.role === 'user' ? 'You' : 'Claude'),
              m.ts ? new Date(m.ts).toLocaleTimeString() : ''),
            h('div', { class: 'msg-body' }, m.blocks.map(renderChatBlock)))))
        : h('p', { class: 'empty' }, 'No messages yet.'));
  }

  async function renderDoc(pane, doc) {
    const url = rawUrl(session.id, doc.file);
    pane.append(h('div', { class: 'pane-head' },
      // Markdown documents open with their own "# Title", so the header
      // shows the file name there instead of repeating it.
      doc.type === 'markdown' ? h('h1', { class: 'file-name' }, doc.file) : h('h1', null, doc.title),
      h('span', { class: 'actions' },
        h('span', { class: 'session-info' }, `${fmtSize(doc.size)} · ${relTime(doc.mtime)}`),
        h('a', { href: url, target: '_blank', rel: 'noopener' }, 'Open raw ↗'),
        h('a', { href: url, download: doc.file }, 'Download'))));

    if (doc.type === 'html') {
      pane.append(h('iframe', { class: 'doc-frame', src: url, sandbox: 'allow-scripts', title: doc.title }));
    } else if (doc.type === 'image') {
      pane.append(h('img', { class: 'doc-image', src: url, alt: doc.title }));
    } else if (doc.type === 'pdf' || doc.type === 'other') {
      pane.append(h('p', { class: 'empty' }, 'No inline preview for this file type. Use "Open raw" or "Download".'));
    } else {
      const text = await (await fetch(url, { cache: 'no-store' })).text();
      if (doc.type === 'markdown') {
        pane.append(renderMarkdown(text, session.id));
      } else if (doc.type === 'code') {
        const code = h('code', { class: codeLanguage(doc.file) ? `language-${codeLanguage(doc.file)}` : null }, text);
        pane.append(h('pre', { class: 'code' }, code));
        hljs.highlightElement(code);
      } else {
        pane.append(h('pre', { class: 'plain' }, text));
      }
    }
  }

  async function renderSession(force) {
    const target = currentTarget();
    const doc = session.docs.find((d) => d.file === target);
    // Re-render only when the selection or its content changed, so polling
    // doesn't reset scroll position or collapse opened tool calls.
    const key = `${target}|${doc ? doc.mtime : session.updatedAt}|${session.docs.map((d) => d.file + d.mtime).join()}`;
    if (!force && key === renderedKey) return;
    const atBottom = window.innerHeight + window.scrollY >= document.body.scrollHeight - 40;
    const sameTarget = renderedKey && renderedKey.split('|')[0] === target;
    renderedKey = key;

    crumb.textContent = `/ ${session.title}`;
    back.hidden = false;
    document.title = `${doc ? doc.title : session.title} · Claude Share`;
    const pane = h('section', { class: 'pane' });
    try {
      if (doc) await renderDoc(pane, doc);
      else await renderChat(pane);
    } catch (err) {
      pane.append(h('p', { class: 'empty' }, `Failed to load: ${err.message}`));
    }
    const scrollY = window.scrollY;
    app.replaceChildren(h('div', { class: 'session' }, renderSidebar(target), pane));
    // Follow a live chat if the reader was already at the bottom; otherwise
    // keep their place on refresh and start new selections from the top.
    if (sameTarget && target === 'chat' && atBottom) window.scrollTo(0, document.body.scrollHeight);
    else window.scrollTo(0, sameTarget ? scrollY : 0);
  }

  async function showSession(id) {
    try {
      session = await getJson(`/share/api/s/${id}`);
    } catch {
      back.hidden = false;
      app.replaceChildren(h('p', { class: 'empty' }, 'Session not found. ', h('a', { href: '/share' }, 'All sessions')));
      return;
    }
    await renderSession(true);
    window.addEventListener('hashchange', () => renderSession(false));
    setInterval(async () => {
      if (document.hidden) return;
      try {
        session = await getJson(`/share/api/s/${id}`);
        await renderSession(false);
      } catch {
        // transient; try again next tick
      }
    }, POLL_MS);
  }

  // --- routing ---------------------------------------------------------------
  const m = location.pathname.match(/^\/share\/s\/([^/]+)\/?$/);
  (m ? showSession(m[1]) : showList()).catch((err) => {
    app.replaceChildren(h('p', { class: 'empty' }, `Failed to load: ${err.message}`));
  });
})();
