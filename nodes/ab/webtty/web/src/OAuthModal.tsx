import { useState } from 'preact/hooks';
import { api } from './api';

interface Props {
  open: boolean;
  onClose: () => void;
}

export function OAuthModal({ open, onClose }: Props) {
  const [url, setUrl] = useState('');
  const [result, setResult] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  if (!open) return null;

  const submit = async (e: Event) => {
    e.preventDefault();
    setError(null);
    setResult(null);
    setBusy(true);
    try {
      const res = await api.relayOAuth(url.trim());
      setResult(`HTTP ${res.status}: ${res.body.slice(0, 300)}`);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div class="modal-backdrop" onClick={onClose}>
      <div class="modal" onClick={(e) => e.stopPropagation()}>
        <div class="modal-header">
          <strong>OAuth Localhost Callback Relay</strong>
          <button onClick={onClose}>&times;</button>
        </div>
        <p class="muted">
          If <code>claude</code> or <code>agy</code> redirects your local browser to{' '}
          <code>http://localhost:&lt;port&gt;/...</code> and fails to connect, paste that full
          redirect URL here to deliver it to the CLI listening on the server.
        </p>
        <form onSubmit={submit}>
          <input
            type="url"
            placeholder="http://localhost:54321/callback?code=...&state=..."
            value={url}
            onInput={(e) => setUrl((e.target as HTMLInputElement).value)}
            required
          />
          <div class="modal-actions">
            <button type="submit" disabled={busy}>
              {busy ? 'Sending…' : 'Deliver Callback'}
            </button>
          </div>
        </form>
        {result && <div class="ok-box">{result}</div>}
        {error && <div class="error-box">{error}</div>}
      </div>
    </div>
  );
}
