import { useEffect, useRef, useState } from 'preact/hooks';
import { Terminal } from '@xterm/xterm';
import { FitAddon } from '@xterm/addon-fit';
import { ClipboardAddon } from '@xterm/addon-clipboard';
import { WebLinksAddon } from '@xterm/addon-web-links';
import { SearchAddon } from '@xterm/addon-search';
import { Unicode11Addon } from '@xterm/addon-unicode11';
import { WebglAddon } from '@xterm/addon-webgl';
import { SerializeAddon } from '@xterm/addon-serialize';

interface Props {
  sessionId: string;
  active: boolean;
  onStatusChange: (status: 'connecting' | 'connected' | 'reconnecting' | 'disconnected') => void;
  onRegisterActions?: (actions: {
    copyScreen: () => void;
    findNext: (q: string) => void;
    findPrev: (q: string) => void;
    clearSearch: () => void;
    focus: () => void;
  }) => void;
}

function copyWithFallback(text: string) {
  const ta = document.createElement('textarea');
  ta.value = text;
  ta.setAttribute('readonly', '');
  ta.style.cssText = 'position:fixed;top:0;left:-9999px;';
  document.body.appendChild(ta);
  ta.focus();
  ta.select();
  try {
    document.execCommand('copy');
  } catch {
    // best effort
  }
  document.body.removeChild(ta);
}

function copyText(text: string) {
  if (!text) return;
  if (navigator.clipboard?.writeText) {
    navigator.clipboard.writeText(text).catch(() => copyWithFallback(text));
  } else {
    copyWithFallback(text);
  }
}

function stripAnsi(str: string): string {
  return str.replace(/\x1b\[[0-9;]*[a-zA-Z]/g, '').replace(/\x1b\][^\x07]*\x07/g, '');
}

export function TerminalPane({ sessionId, active, onStatusChange, onRegisterActions }: Props) {
  const containerRef = useRef<HTMLDivElement>(null);
  const fitRef = useRef<FitAddon | null>(null);
  const termRef = useRef<Terminal | null>(null);
  const wsRef = useRef<WebSocket | null>(null);
  const [exitedCode, setExitedCode] = useState<number | null>(null);

  useEffect(() => {
    const el = containerRef.current;
    if (!el) return;

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

    const fitAddon = new FitAddon();
    const clipboardAddon = new ClipboardAddon();
    const webLinksAddon = new WebLinksAddon();
    const searchAddon = new SearchAddon();
    const unicode11Addon = new Unicode11Addon();
    const serializeAddon = new SerializeAddon();

    term.loadAddon(fitAddon);
    term.loadAddon(clipboardAddon);
    term.loadAddon(webLinksAddon);
    term.loadAddon(searchAddon);
    term.loadAddon(unicode11Addon);
    term.loadAddon(serializeAddon);
    term.unicode.activeVersion = '11';

    term.open(el);
    try {
      const webglAddon = new WebglAddon();
      webglAddon.onContextLoss(() => webglAddon.dispose());
      term.loadAddon(webglAddon);
    } catch {
      // Fallback to canvas renderer
    }

    fitAddon.fit();
    termRef.current = term;
    fitRef.current = fitAddon;

    // Shift+Enter newline support for Claude Code / TUI CLIs
    term.attachCustomKeyEventHandler((event) => {
      if (
        event.type === 'keydown' &&
        event.key === 'Enter' &&
        event.shiftKey &&
        !event.ctrlKey &&
        !event.altKey &&
        !event.metaKey
      ) {
        const ws = wsRef.current;
        if (ws && ws.readyState === WebSocket.OPEN) {
          ws.send(JSON.stringify({ type: 'data', data: '\x1b\r' }));
        }
        return false;
      }
      return true;
    });

    // Mouse-selection clipboard bridge (from term.ab18.in)
    let dragging = false;
    let pendingSelection: string | null = null;
    const flushSelectionCopy = () => {
      const text = pendingSelection;
      pendingSelection = null;
      if (text) copyText(text);
    };
    term.onSelectionChange(() => {
      const sel = term.getSelection();
      if (!sel) return;
      pendingSelection = sel;
      if (!dragging) flushSelectionCopy();
    });
    const onMouseDown = () => { dragging = true; };
    const onMouseUp = () => {
      if (!dragging) return;
      dragging = false;
      flushSelectionCopy();
    };
    term.element?.addEventListener('mousedown', onMouseDown, true);
    window.addEventListener('mouseup', onMouseUp, true);

    // WebSocket connection with backoff reconnect
    let closedByUnmount = false;
    let reconnectDelayMs = 500;
    let reconnectTimer: number | undefined;

    const sendResize = () => {
      const ws = wsRef.current;
      if (ws && ws.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify({ type: 'resize', cols: term.cols, rows: term.rows }));
      }
    };

    const connect = () => {
      if (closedByUnmount) return;
      onStatusChange('connecting');
      const proto = location.protocol === 'https:' ? 'wss' : 'ws';
      const ws = new WebSocket(`${proto}://${location.host}/ws/${encodeURIComponent(sessionId)}`);
      wsRef.current = ws;

      ws.addEventListener('open', () => {
        onStatusChange('connected');
        reconnectDelayMs = 500;
        fitAddon.fit();
        sendResize();
      });

      ws.addEventListener('message', (event) => {
        try {
          const msg = JSON.parse(event.data);
          if (msg.type === 'data' && typeof msg.data === 'string') {
            term.write(msg.data);
          } else if (msg.type === 'replay' && typeof msg.data === 'string') {
            term.reset();
            term.write(msg.data);
          } else if (msg.type === 'exit') {
            setExitedCode(typeof msg.code === 'number' ? msg.code : 0);
          }
        } catch {
          // ignore malformed frames
        }
      });

      ws.addEventListener('close', () => {
        if (closedByUnmount) return;
        onStatusChange('reconnecting');
        reconnectTimer = window.setTimeout(connect, reconnectDelayMs);
        reconnectDelayMs = Math.min(reconnectDelayMs * 2, 8000);
      });

      ws.addEventListener('error', () => {
        onStatusChange('disconnected');
        ws.close();
      });
    };

    const dataDisp = term.onData((data) => {
      const ws = wsRef.current;
      if (ws && ws.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify({ type: 'data', data }));
      }
    });

    const ro = new ResizeObserver(() => {
      if (el.offsetParent !== null) {
        fitAddon.fit();
        sendResize();
      }
    });
    ro.observe(el);

    onRegisterActions?.({
      copyScreen: () => copyText(stripAnsi(serializeAddon.serialize({ scrollback: 0 }))),
      findNext: (q: string) => searchAddon.findNext(q),
      findPrev: (q: string) => searchAddon.findPrevious(q),
      clearSearch: () => searchAddon.clearDecorations(),
      focus: () => term.focus(),
    });

    connect();

    return () => {
      closedByUnmount = true;
      if (reconnectTimer) clearTimeout(reconnectTimer);
      ro.disconnect();
      dataDisp.dispose();
      term.element?.removeEventListener('mousedown', onMouseDown, true);
      window.removeEventListener('mouseup', onMouseUp, true);
      wsRef.current?.close();
      term.dispose();
    };
  }, [sessionId]);

  useEffect(() => {
    if (active) {
      requestAnimationFrame(() => {
        fitRef.current?.fit();
        const ws = wsRef.current;
        const term = termRef.current;
        if (ws && ws.readyState === WebSocket.OPEN && term) {
          ws.send(JSON.stringify({ type: 'resize', cols: term.cols, rows: term.rows }));
        }
        term?.focus();
      });
    }
  }, [active]);

  return (
    <div class={`terminal-pane ${active ? 'active' : 'hidden'}`}>
      {exitedCode !== null && (
        <div class="exit-banner">Session exited (code {exitedCode})</div>
      )}
      <div class="terminal-host" ref={containerRef} />
    </div>
  );
}
