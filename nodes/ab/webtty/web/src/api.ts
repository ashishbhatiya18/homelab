import type {
  FsListResponse,
  FsReadResponse,
  GitCommit,
  GitRepo,
  GitStatusResponse,
  ServerConfig,
  SessionInfo,
  SessionProfile,
} from './types';

async function requestJson<T>(url: string, init?: RequestInit): Promise<T> {
  const res = await fetch(url, init);
  if (!res.ok) {
    const text = await res.text();
    throw new Error(text || `HTTP ${res.status}`);
  }
  return res.json() as Promise<T>;
}

export const api = {
  getConfig: () => requestJson<ServerConfig>('/api/config'),

  listSessions: () => requestJson<SessionInfo[]>('/api/sessions'),

  createSession: (profile: SessionProfile, cwd?: string, tmux?: boolean) =>
    requestJson<SessionInfo>('/api/sessions', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ profile, cwd, tmux: Boolean(tmux) }),
    }),

  deleteSession: (id: string) =>
    fetch(`/api/sessions/${encodeURIComponent(id)}`, { method: 'DELETE' }),

  listDir: (path?: string) =>
    requestJson<FsListResponse>(
      `/api/fs/list${path ? `?path=${encodeURIComponent(path)}` : ''}`,
    ),

  readFile: (path: string) =>
    requestJson<FsReadResponse>(`/api/fs/read?path=${encodeURIComponent(path)}`),

  writeFile: (path: string, content: string) =>
    requestJson<{ ok: boolean }>('/api/fs/write', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ path, content }),
    }),

  listGitRepos: () => requestJson<GitRepo[]>('/api/git/repos'),

  gitStatus: (repo: string) =>
    requestJson<GitStatusResponse>(`/api/git/status?repo=${encodeURIComponent(repo)}`),

  gitDiff: (repo: string, mode: 'unstaged' | 'staged' | 'all', file?: string) => {
    const params = new URLSearchParams({ repo, mode });
    if (file) params.set('file', file);
    return requestJson<{ diff: string }>(`/api/git/diff?${params.toString()}`);
  },

  gitLog: (repo: string) =>
    requestJson<GitCommit[]>(`/api/git/log?repo=${encodeURIComponent(repo)}`),

  gitShow: (repo: string, commit: string) =>
    requestJson<{ diff: string }>(
      `/api/git/show?repo=${encodeURIComponent(repo)}&commit=${encodeURIComponent(commit)}`,
    ),

  relayOAuth: (url: string) =>
    requestJson<{ status: number; body: string }>('/api/oauth/relay', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ url }),
    }),
};
