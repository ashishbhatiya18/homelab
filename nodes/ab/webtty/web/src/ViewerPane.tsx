import { useEffect, useState } from 'preact/hooks';
import { html as diff2html } from 'diff2html';
import hljs from 'highlight.js';
import { api } from './api';

export type ViewerTarget =
  | { kind: 'file'; path: string }
  | { kind: 'diff'; repo: string; mode: 'unstaged' | 'staged' | 'all'; file?: string }
  | { kind: 'commit'; repo: string; commit: string }
  | null;

interface Props {
  target: ViewerTarget;
  allowWrite: boolean;
  onClose: () => void;
}

export function ViewerPane({ target, allowWrite, onClose }: Props) {
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [fileHtml, setFileHtml] = useState<string>('');
  const [rawText, setRawText] = useState<string>('');
  const [editing, setEditing] = useState(false);
  const [saving, setSaving] = useState(false);
  const [diffHtml, setDiffHtml] = useState<string>('');
  const [sideBySide, setSideBySide] = useState(false);
  const [rawDiff, setRawDiff] = useState<string>('');

  useEffect(() => {
    if (!target) return;
    setError(null);
    setEditing(false);
    setLoading(true);

    if (target.kind === 'file') {
      api
        .readFile(target.path)
        .then((res) => {
          if (res.binary) {
            setRawText('');
            setFileHtml('<div class="muted pad">Binary file preview unavailable.</div>');
            return;
          }
          setRawText(res.content);
          const highlighted = hljs.highlightAuto(res.content).value;
          setFileHtml(`<pre class="hljs code-view"><code>${highlighted}</code></pre>`);
        })
        .catch((e) => setError(e instanceof Error ? e.message : String(e)))
        .finally(() => setLoading(false));
    } else if (target.kind === 'diff') {
      api
        .gitDiff(target.repo, target.mode, target.file)
        .then((res) => setRawDiff(res.diff))
        .catch((e) => setError(e instanceof Error ? e.message : String(e)))
        .finally(() => setLoading(false));
    } else if (target.kind === 'commit') {
      api
        .gitShow(target.repo, target.commit)
        .then((res) => setRawDiff(res.diff))
        .catch((e) => setError(e instanceof Error ? e.message : String(e)))
        .finally(() => setLoading(false));
    }
  }, [target]);

  useEffect(() => {
    if (!target || target.kind === 'file') return;
    if (!rawDiff.trim()) {
      setDiffHtml('<div class="muted pad">No diff output.</div>');
      return;
    }
    const rendered = diff2html(rawDiff, {
      drawFileList: true,
      matching: 'lines',
      outputFormat: sideBySide ? 'side-by-side' : 'line-by-line',
    });
    setDiffHtml(rendered);
  }, [rawDiff, sideBySide, target]);

  if (!target) return null;

  const title =
    target.kind === 'file'
      ? target.path
      : target.kind === 'diff'
        ? `${target.repo} (${target.mode}${target.file ? `: ${target.file}` : ''})`
        : `${target.repo} @ ${target.commit.slice(0, 8)}`;

  const saveFile = async () => {
    if (target.kind !== 'file') return;
    try {
      setSaving(true);
      await api.writeFile(target.path, rawText);
      const highlighted = hljs.highlightAuto(rawText).value;
      setFileHtml(`<pre class="hljs code-view"><code>${highlighted}</code></pre>`);
      setEditing(false);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setSaving(false);
    }
  };

  return (
    <section class="viewer-pane">
      <div class="viewer-header">
        <span class="viewer-title" title={title}>{title}</span>
        <div class="viewer-actions">
          {target.kind === 'file' && allowWrite && !editing && (
            <button onClick={() => setEditing(true)}>Edit</button>
          )}
          {target.kind === 'file' && allowWrite && editing && (
            <button onClick={saveFile} disabled={saving}>{saving ? 'Saving…' : 'Save'}</button>
          )}
          {target.kind !== 'file' && (
            <button onClick={() => setSideBySide((v) => !v)}>
              {sideBySide ? 'Unified' : 'Side-by-side'}
            </button>
          )}
          <button onClick={onClose}>&times;</button>
        </div>
      </div>

      <div class="viewer-body">
        {loading && <div class="muted pad">Loading…</div>}
        {error && <div class="error-box">{error}</div>}
        {!loading && !error && target.kind === 'file' && (
          editing ? (
            <textarea
              class="file-editor"
              value={rawText}
              onInput={(e) => setRawText((e.target as HTMLTextAreaElement).value)}
            />
          ) : (
            <div dangerouslySetInnerHTML={{ __html: fileHtml }} />
          )
        )}
        {!loading && !error && target.kind !== 'file' && (
          <div class="diff-wrap d2h-dark-color-scheme" dangerouslySetInnerHTML={{ __html: diffHtml }} />
        )}
      </div>
    </section>
  );
}
