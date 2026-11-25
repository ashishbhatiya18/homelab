import { render } from 'preact';
import { useEffect, useRef, useState } from 'preact/hooks';
import '@xterm/xterm/css/xterm.css';
import 'diff2html/bundles/css/diff2html.min.css';
import 'highlight.js/styles/github-dark.css';
import './styles.css';

import { api } from './api';
import type { ServerConfig, SessionInfo, SessionProfile } from './types';
import { TerminalPane } from './TerminalPane';
import { Sidebar } from './Sidebar';
import { ViewerPane, type ViewerTarget } from './ViewerPane';
import { OAuthModal } from './OAuthModal';

function App() {
  const [config, setConfig] = useState<ServerConfig>({
    workspaceRoot: '',
    allowFileWrite: false,
    allowOAuthRelay: true,
  });
  const [sessions, setSessions] = useState<SessionInfo[]>([]);
  const [activeId, setActiveId] = useState<string>('');
  const [status, setStatus] = useState<'connecting' | 'connected' | 'reconnecting' | 'disconnected'>('connecting');
  const [sidebarOpen, setSidebarOpen] = useState(true);
  const [sidebarTab, setSidebarTab] = useState<'files' | 'git'>('files');
  const [viewerTarget, setViewerTarget] = useState<ViewerTarget>(null);
  const [searchOpen, setSearchOpen] = useState(false);
  const [searchQuery, setSearchQuery] = useState('');
  const [oauthOpen, setOAuthOpen] = useState(false);
  const [tmuxMode, setTmuxMode] = useState(false);

  const actionsRef = useRef<Record<string, {
    copyScreen: () => void;
    findNext: (q: string) => void;
    findPrev: (q: string) => void;
    clearSearch: () => void;
    focus: () => void;
  }>>({});

  const launchSession = async (profile: SessionProfile, cwd?: string) => {
    const created = await api.createSession(profile, cwd || config.workspaceRoot || undefined, tmuxMode);
    setSessions((prev) => [...prev, created]);
    setActiveId(created.id);
  };

  const closeSession = async (id: string) => {
    await api.deleteSession(id);
    setSessions((prev) => {
      const next = prev.filter((s) => s.id !== id);
      if (activeId === id) {
        setActiveId(next[0]?.id || '');
      }
      return next;
    });
  };

  useEffect(() => {
    (async () => {
      const [cfg, existing] = await Promise.all([api.getConfig(), api.listSessions()]);
      setConfig(cfg);
      if (existing.length > 0) {
        setSessions(existing);
        setActiveId(existing[0].id);
      } else {
        const first = await api.createSession('claude', cfg.workspaceRoot, false);
        setSessions([first]);
        setActiveId(first.id);
      }
    })();
  }, []);

  useEffect(() => {
    const onKeyDown = (e: KeyboardEvent) => {
      if ((e.ctrlKey || e.metaKey) && e.shiftKey && e.key.toLowerCase() === 'f') {
        e.preventDefault();
        setSearchOpen(true);
      }
    };
    window.addEventListener('keydown', onKeyDown);
    return () => window.removeEventListener('keydown', onKeyDown);
  }, []);

  return (
    <div class="layout">
      <header class="topbar">
        <div class="topbar-left">
          <span class={`status-pill ${status}`}>{status}</span>
          <div class="tabs">
            {sessions.map((s) => (
              <div
                key={s.id}
                class={`tab ${s.id === activeId ? 'active' : ''}`}
                onClick={() => setActiveId(s.id)}
                title={`${s.profile} — ${s.cwd}`}
              >
                <span>{s.title}</span>
                <button
                  class="tab-close"
                  onClick={(e) => {
                    e.stopPropagation();
                    void closeSession(s.id);
                  }}
                >
                  &times;
                </button>
              </div>
            ))}
          </div>
          <div class="launch-group">
            <button onClick={() => void launchSession('claude')}>+ Claude</button>
            <button onClick={() => void launchSession('agy')}>+ agy</button>
            <button onClick={() => void launchSession('shell')}>+ Shell</button>
            <label class="tmux-toggle" title="Run new tabs inside tmux on host">
              <input
                type="checkbox"
                checked={tmuxMode}
                onChange={(e) => setTmuxMode((e.target as HTMLInputElement).checked)}
              />
              tmux
            </label>
          </div>
        </div>

        <div class="topbar-right">
          <button onClick={() => setSidebarOpen((v) => !v)}>
            {sidebarOpen ? 'Hide Panel' : 'Files / Git'}
          </button>
          <button onClick={() => setSearchOpen((v) => !v)}>Search</button>
          <button onClick={() => actionsRef.current[activeId]?.copyScreen()}>Copy Screen</button>
          {config.allowOAuthRelay && (
            <button onClick={() => setOAuthOpen(true)}>OAuth Relay</button>
          )}
        </div>
      </header>

      {searchOpen && (
        <div class="search-bar">
          <input
            type="text"
            placeholder="Search terminal scrollback…"
            value={searchQuery}
            onInput={(e) => setSearchQuery((e.target as HTMLInputElement).value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter') actionsRef.current[activeId]?.findNext(searchQuery);
              if (e.key === 'Escape') {
                setSearchOpen(false);
                actionsRef.current[activeId]?.clearSearch();
                actionsRef.current[activeId]?.focus();
              }
            }}
            autoFocus
          />
          <button onClick={() => actionsRef.current[activeId]?.findPrev(searchQuery)}>&uarr;</button>
          <button onClick={() => actionsRef.current[activeId]?.findNext(searchQuery)}>&darr;</button>
          <button
            onClick={() => {
              setSearchOpen(false);
              actionsRef.current[activeId]?.clearSearch();
              actionsRef.current[activeId]?.focus();
            }}
          >
            &times;
          </button>
        </div>
      )}

      <div class="workspace">
        {sidebarOpen && (
          <Sidebar
            tab={sidebarTab}
            onTabChange={setSidebarTab}
            workspaceRoot={config.workspaceRoot}
            onOpenFile={(path) => setViewerTarget({ kind: 'file', path })}
            onOpenDiff={(repo, mode, file) => setViewerTarget({ kind: 'diff', repo, mode, file })}
            onOpenCommitDiff={(repo, commit) => setViewerTarget({ kind: 'commit', repo, commit })}
            onLaunchInDir={(profile, cwd) => void launchSession(profile, cwd)}
          />
        )}

        <main class="main-area">
          <div class={`terminals-wrap ${viewerTarget ? 'split' : ''}`}>
            {sessions.map((s) => (
              <TerminalPane
                key={s.id}
                sessionId={s.id}
                active={s.id === activeId}
                onStatusChange={(st) => {
                  if (s.id === activeId) setStatus(st);
                }}
                onRegisterActions={(acts) => {
                  actionsRef.current[s.id] = acts;
                }}
              />
            ))}
          </div>

          <ViewerPane
            target={viewerTarget}
            allowWrite={config.allowFileWrite}
            onClose={() => setViewerTarget(null)}
          />
        </main>
      </div>

      <OAuthModal open={oauthOpen} onClose={() => setOAuthOpen(false)} />
    </div>
  );
}

render(<App />, document.getElementById('app')!);
