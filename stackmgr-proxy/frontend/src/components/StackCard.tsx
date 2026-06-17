'use client';

import { useState } from 'react';
import {
  Play, Square, RotateCcw, Hammer, ArrowUpCircle,
  ScrollText, ChevronRight, X, Loader2, ExternalLink,
} from 'lucide-react';
import { Stack, Service } from '@/types';
import { stacksAPI } from '@/lib/api';

type StackAction = 'start' | 'stop' | 'restart' | 'rebuild' | 'update';
type ContainerAction = 'start' | 'stop' | 'restart';

function Btn({ title, onClick, disabled, active, variant = 'ghost', children }: {
  title: string;
  onClick: (e: React.MouseEvent) => void;
  disabled?: boolean;
  active?: boolean;
  variant?: 'ghost' | 'success' | 'danger' | 'primary' | 'warning';
  children: React.ReactNode;
}) {
  const base = 'rounded p-1.5 transition-colors disabled:opacity-40';
  const cls = active
    ? 'bg-blue-500/20 text-blue-400'
    : {
        ghost:   'text-slate-500 hover:text-white hover:bg-slate-700',
        success: 'text-emerald-500 hover:text-emerald-400 hover:bg-emerald-500/15',
        danger:  'text-red-500 hover:text-red-400 hover:bg-red-500/15',
        primary: 'text-blue-400 hover:text-blue-300 hover:bg-blue-500/15',
        warning: 'text-amber-500 hover:text-amber-400 hover:bg-amber-500/15',
      }[variant];
  return (
    <button title={title} onClick={(e) => { e.stopPropagation(); onClick(e); }} disabled={disabled} className={`${base} ${cls}`}>
      {children}
    </button>
  );
}

function LogPanel({ text, loading, onClose }: { text: string; loading: boolean; onClose: () => void }) {
  return (
    <div className="border-t border-slate-700/50 bg-black/50">
      <div className="flex items-center justify-between px-3 py-1 border-b border-slate-800">
        <span className="text-xs text-slate-500 font-mono">logs</span>
        <button
          onClick={(e) => { e.stopPropagation(); onClose(); }}
          className="text-slate-600 hover:text-slate-400 transition-colors p-0.5"
        >
          <X size={12} />
        </button>
      </div>
      {loading ? (
        <div className="flex items-center gap-2 px-3 py-4 text-slate-600 text-xs">
          <Loader2 size={12} className="animate-spin" /> fetching…
        </div>
      ) : (
        <pre className="px-3 py-3 text-xs text-slate-300 font-mono overflow-auto max-h-72 whitespace-pre-wrap break-all leading-relaxed">
          {text || '(no output)'}
        </pre>
      )}
    </div>
  );
}

function useLogPanel(fetcher: () => Promise<string>) {
  const [open, setOpen] = useState(false);
  const [text, setText] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);

  const toggle = async (e: React.MouseEvent) => {
    e.stopPropagation();
    if (open) { setOpen(false); return; }
    setOpen(true);
    if (text !== null) return;
    setLoading(true);
    try { setText(await fetcher()); }
    catch { setText('(error fetching logs)'); }
    finally { setLoading(false); }
  };

  return { open, loading, text: text ?? '', toggle, close: () => setOpen(false) };
}

function ContainerRow({ svc, stack }: { svc: Service; stack: Stack }) {
  const [actLoading, setActLoading] = useState(false);
  const [msg, setMsg] = useState<string | null>(null);
  const logs = useLogPanel(async () => {
    const res = await stacksAPI.getContainerLogs(stack.node, stack.name, svc.name);
    return res.data.logs ?? '';
  });

  const act = async (action: ContainerAction) => {
    setActLoading(true);
    try {
      if (action === 'start') await stacksAPI.startContainer(stack.node, stack.name, svc.name);
      if (action === 'stop') await stacksAPI.stopContainer(stack.node, stack.name, svc.name);
      if (action === 'restart') await stacksAPI.restartContainer(stack.node, stack.name, svc.name);
      setMsg(`${action} ok`);
      setTimeout(() => setMsg(null), 3000);
    } catch {
      setMsg('failed');
    } finally {
      setActLoading(false);
    }
  };

  const state = svc.state ?? svc.status;
  const isRunning = state === 'running';
  const isStopped = state === 'exited' || state === 'stopped';

  const dotColor = isRunning ? 'bg-emerald-400' : isStopped ? 'bg-red-400' : 'bg-amber-400';
  const stateColor = isRunning ? 'text-emerald-400' : isStopped ? 'text-red-400' : 'text-amber-400';

  // Red gradient highlight for non-running containers
  const rowBg = isRunning
    ? 'bg-slate-950/40 hover:bg-slate-800/20'
    : isStopped
      ? 'bg-gradient-to-r from-red-900/30 to-transparent hover:from-red-900/40'
      : 'bg-gradient-to-r from-amber-900/30 to-transparent hover:from-amber-900/40';

  return (
    <div>
      <div className={`flex items-center gap-3 pl-8 pr-4 py-2 border-t border-slate-800/50 transition-colors ${rowBg}`}>
        <div className={`w-1.5 h-1.5 rounded-full shrink-0 ${dotColor}`} />

        <span className="font-mono text-sm text-slate-300 min-w-[140px] shrink-0">{svc.name}</span>
        <span className={`text-xs shrink-0 ${stateColor}`}>{state}</span>

        {/* Docker image */}
        {svc.image && (
          <span className="text-xs font-mono text-slate-500 shrink-0 hidden sm:block" title={`${svc.image}${svc.imageTag ? ':' + svc.imageTag : ''}`}>
            <span className="text-slate-600">{svc.image.includes('/') ? svc.image.split('/').slice(-1)[0] : svc.image}</span>
            {svc.imageTag && svc.imageTag !== 'latest' && (
              <span className="text-blue-500/70">:{svc.imageTag}</span>
            )}
          </span>
        )}

        {/* Traefik URL */}
        {svc.url && (
          <a
            href={svc.url}
            target="_blank"
            rel="noopener noreferrer"
            onClick={(e) => e.stopPropagation()}
            className="flex items-center gap-1 text-xs text-slate-500 hover:text-blue-400 transition-colors font-mono shrink-0"
            title={svc.url}
          >
            <ExternalLink size={11} />
            <span className="truncate max-w-[160px]">{svc.url.replace('https://', '')}</span>
          </a>
        )}

        <div className="flex-1 text-xs">{msg && <span className="text-blue-400">{msg}</span>}</div>

        <div className="flex items-center gap-0.5 shrink-0">
          <Btn title="Start" onClick={() => act('start')} disabled={actLoading} variant="success"><Play size={13} /></Btn>
          <Btn title="Stop" onClick={() => act('stop')} disabled={actLoading} variant="danger"><Square size={13} /></Btn>
          <Btn title="Restart" onClick={() => act('restart')} disabled={actLoading} variant="primary"><RotateCcw size={13} /></Btn>
          <Btn title="Logs" active={logs.open} onClick={logs.toggle} disabled={logs.loading} variant="ghost"><ScrollText size={13} /></Btn>
        </div>
      </div>
      {logs.open && <LogPanel text={logs.text} loading={logs.loading} onClose={logs.close} />}
    </div>
  );
}

export default function StackCard({ stack }: { stack: Stack }) {
  const [open, setOpen] = useState(false);
  const [actLoading, setActLoading] = useState(false);
  const [msg, setMsg] = useState<string | null>(null);
  const logs = useLogPanel(async () => {
    const res = await stacksAPI.getLogs(stack.node, stack.name);
    return res.data.logs ?? '';
  });

  const services = stack.services ?? [];
  const runningCount = services.filter(s => (s.state ?? s.status) === 'running').length;

  const isRunning = stack.status === 'running';
  const isPartial = stack.status === 'partial';

  const leftBorder = isRunning ? 'border-l-emerald-500' : isPartial ? 'border-l-amber-500' : 'border-l-red-700';
  const dotColor = isRunning ? 'bg-emerald-400' : isPartial ? 'bg-amber-400' : 'bg-red-500';

  // Gradient on header row when not fully running
  const headerBg = isRunning
    ? 'hover:bg-slate-800/30'
    : isPartial
      ? 'bg-gradient-to-r from-amber-900/25 to-transparent hover:from-amber-900/35'
      : 'bg-gradient-to-r from-red-900/30 to-transparent hover:from-red-900/40';

  const handleAction = async (action: StackAction) => {
    setActLoading(true);
    try {
      if (action === 'start') await stacksAPI.startStack(stack.node, stack.name);
      if (action === 'stop') await stacksAPI.stopStack(stack.node, stack.name);
      if (action === 'restart') await stacksAPI.restartStack(stack.node, stack.name);
      if (action === 'rebuild') await stacksAPI.rebuildStack(stack.node, stack.name);
      if (action === 'update') await stacksAPI.updateStack(stack.node, stack.name);
      setMsg(`${action} ok`);
      setTimeout(() => setMsg(null), 3000);
    } catch {
      setMsg(`${action} failed`);
    } finally {
      setActLoading(false);
    }
  };

  return (
    <div className={`bg-slate-900 border border-slate-800 border-l-2 ${leftBorder} rounded-lg overflow-hidden`}>
      {/* Stack header */}
      <div
        className={`flex items-center gap-3 px-4 py-3 cursor-pointer select-none transition-colors ${headerBg}`}
        onClick={() => setOpen(o => !o)}
      >
        <ChevronRight
          size={14}
          className={`text-slate-500 shrink-0 transition-transform duration-150 ${open ? 'rotate-90' : ''}`}
        />

        <div className={`w-2 h-2 rounded-full shrink-0 ${dotColor}`} />

        {/* name + path */}
        <div className="min-w-[160px] shrink-0">
          <span className="text-sm font-semibold text-white">{stack.name}</span>
          <p className="text-xs text-slate-600 font-mono truncate max-w-[200px]" title={stack.path}>
            …/{stack.path?.split('/').slice(-2).join('/')}
            {stack.composeFile && stack.composeFile !== 'docker-compose.yml' && stack.composeFile !== 'compose.yml' && (
              <span className="text-slate-700 ml-1">({stack.composeFile})</span>
            )}
          </p>
        </div>

        {/* running count pill */}
        {services.length > 0 && (
          <span className={`text-xs px-2 py-0.5 rounded-full border shrink-0 tabular-nums ${
            runningCount === services.length
              ? 'bg-emerald-500/10 text-emerald-400 border-emerald-500/30'
              : runningCount === 0
                ? 'bg-red-500/10 text-red-400 border-red-500/30'
                : 'bg-amber-500/10 text-amber-400 border-amber-500/30'
          }`}>
            {runningCount}/{services.length}
          </span>
        )}

        <div className="flex-1 text-xs">{msg && <span className="text-blue-400">{msg}</span>}</div>

        {/* stack actions */}
        <div className="flex items-center gap-0.5 shrink-0">
          <Btn title="Start" onClick={() => handleAction('start')} disabled={actLoading} variant="success"><Play size={14} /></Btn>
          <Btn title="Stop" onClick={() => handleAction('stop')} disabled={actLoading} variant="danger"><Square size={14} /></Btn>
          <Btn title="Restart" onClick={() => handleAction('restart')} disabled={actLoading} variant="primary"><RotateCcw size={14} /></Btn>
          <Btn title="Rebuild" onClick={() => handleAction('rebuild')} disabled={actLoading} variant="warning"><Hammer size={14} /></Btn>
          <Btn title="Update" onClick={() => handleAction('update')} disabled={actLoading} variant="primary"><ArrowUpCircle size={14} /></Btn>
          <Btn title="Logs" active={logs.open} onClick={logs.toggle} disabled={logs.loading} variant="ghost"><ScrollText size={14} /></Btn>
        </div>
      </div>

      {/* Stack log panel */}
      {logs.open && <LogPanel text={logs.text} loading={logs.loading} onClose={logs.close} />}

      {/* Container rows */}
      {open && services.length > 0 && (
        <div>
          {services.map(svc => (
            <ContainerRow key={svc.name} svc={svc} stack={stack} />
          ))}
        </div>
      )}
    </div>
  );
}
