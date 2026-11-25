import { useEffect, useState } from 'preact/hooks';
import { api } from './api';
import type { FsEntry, GitCommit, GitRepo, GitStatusResponse, SessionProfile } from './types';

interface Props {
  tab: 'files' | 'git';
  onTabChange: (tab: 'files' | 'git') => void;
  workspaceRoot: string;
  onOpenFile: (path: string) => void;
  onOpenDiff: (repo: string, mode: 'unstaged' | 'staged' | 'all', file?: string) => void;
  onOpenCommitDiff: (repo: string, commit: string) => void;
  onLaunchInDir: (profile: SessionProfile, cwd: string) => void;
}

export function Sidebar({
  tab,
  onTabChange,
  workspaceRoot,
  onOpenFile,
  onOpenDiff,
  onOpenCommitDiff,
  onLaunchInDir,
}: Props) {
  // File explorer state
  const [expanded, setExpanded] = useState<Record<string, FsEntry[]>>({});
  const [openDirs, setOpenDirs] = useState<Record<string, boolean>>({});
  const [fsError, setFsError] = useState<string | null>(null);

  // Git state
  const [repos, setRepos] = useState<GitRepo[]>([]);
  const [selectedRepo, setSelectedRepo] = useState<string>('');
  const [gitStatus, setGitStatus] = useState<GitStatusResponse | null>(null);
  const [commits, setCommits] = useState<GitCommit[]>([]);
  const [gitError, setGitError] = useState<string | null>(null);

  const loadDir = async (dirPath: string) => {
    try {
      setFsError(null);
      const res = await api.listDir(dirPath);
      setExpanded((prev) => ({ ...prev, [res.path]: res.entries }));
      setOpenDirs((prev) => ({ ...prev, [res.path]: true }));
    } catch (err) {
      setFsError(err instanceof Error ? err.message : String(err));
    }
  };

  const toggleDir = (dirPath: string) => {
    if (openDirs[dirPath]) {
      setOpenDirs((prev) => ({ ...prev, [dirPath]: false }));
    } else if (expanded[dirPath]) {
      setOpenDirs((prev) => ({ ...prev, [dirPath]: true }));
    } else {
      void loadDir(dirPath);
    }
  };

  const loadRepos = async () => {
    try {
      setGitError(null);
      const list = await api.listGitRepos();
      setRepos(list);
      if (list.length > 0 && !selectedRepo) {
        setSelectedRepo(list[0].path);
      }
    } catch (err) {
      setGitError(err instanceof Error ? err.message : String(err));
    }
  };

  const loadRepoDetails = async (repoPath: string) => {
    if (!repoPath) return;
    try {
      setGitError(null);
      const [status, log] = await Promise.all([api.gitStatus(repoPath), api.gitLog(repoPath)]);
      setGitStatus(status);
      setCommits(log);
    } catch (err) {
      setGitError(err instanceof Error ? err.message : String(err));
    }
  };

  useEffect(() => {
    if (workspaceRoot && !expanded[workspaceRoot]) {
      void loadDir(workspaceRoot);
    }
  }, [workspaceRoot]);

  useEffect(() => {
    if (tab === 'git') {
      void loadRepos();
    }
  }, [tab]);

  useEffect(() => {
    if (tab === 'git' && selectedRepo) {
      void loadRepoDetails(selectedRepo);
    }
  }, [tab, selectedRepo]);

  const renderTree = (dirPath: string, depth = 0) => {
    const entries = expanded[dirPath] || [];
    return entries.map((entry) => {
      const isOpen = Boolean(openDirs[entry.path]);
      return (
        <div key={entry.path}>
          <div
            class="tree-row"
            style={{ paddingLeft: `${8 + depth * 14}px` }}
            onClick={() => (entry.isDir ? toggleDir(entry.path) : onOpenFile(entry.path))}
            title={entry.path}
          >
            <span class="tree-icon">{entry.isDir ? (isOpen ? '▾' : '▸') : '·'}</span>
            <span class={`tree-name ${entry.isDir ? 'dir' : 'file'}`}>{entry.name}</span>
            {entry.isDir && (
              <button
                class="mini-btn"
                title="Open Claude Code in this folder"
                onClick={(e) => {
                  e.stopPropagation();
                  onLaunchInDir('claude', entry.path);
                }}
              >
                +C
              </button>
            )}
          </div>
          {entry.isDir && isOpen && renderTree(entry.path, depth + 1)}
        </div>
      );
    });
  };

  return (
    <aside class="sidebar">
      <div class="sidebar-tabs">
        <button class={tab === 'files' ? 'active' : ''} onClick={() => onTabChange('files')}>
          Files
        </button>
        <button class={tab === 'git' ? 'active' : ''} onClick={() => onTabChange('git')}>
          Git Diff
        </button>
      </div>

      {tab === 'files' ? (
        <div class="sidebar-body">
          <div class="sidebar-header">
            <span class="muted" title={workspaceRoot}>{workspaceRoot || '/workspace'}</span>
            <button class="mini-btn" onClick={() => workspaceRoot && loadDir(workspaceRoot)}>
              ↻
            </button>
          </div>
          {fsError && <div class="error-box">{fsError}</div>}
          <div class="tree-list">{workspaceRoot && renderTree(workspaceRoot)}</div>
        </div>
      ) : (
        <div class="sidebar-body">
          <div class="sidebar-header">
            <select
              class="repo-select"
              value={selectedRepo}
              onChange={(e) => setSelectedRepo((e.target as HTMLSelectElement).value)}
            >
              {repos.length === 0 && <option value="">No git repos found</option>}
              {repos.map((r) => (
                <option value={r.path}>{r.name}</option>
              ))}
            </select>
            <button class="mini-btn" onClick={() => selectedRepo && loadRepoDetails(selectedRepo)}>
              ↻
            </button>
          </div>

          {gitError && <div class="error-box">{gitError}</div>}

          {gitStatus && (
            <div class="git-section">
              <div class="git-branch-row">
                <span class="branch-pill">{gitStatus.branch || 'HEAD'}</span>
                <button class="mini-btn" onClick={() => onOpenDiff(selectedRepo, 'all')}>
                  All Diff
                </button>
              </div>

              <div class="section-label">Changes ({gitStatus.files.length})</div>
              {gitStatus.files.length === 0 && <div class="muted pad">Working tree clean</div>}
              {gitStatus.files.map((f) => {
                const mode = f.indexStatus !== ' ' && f.indexStatus !== '?' ? 'staged' : 'unstaged';
                return (
                  <div
                    class="git-file-row"
                    onClick={() => onOpenDiff(selectedRepo, mode, f.path)}
                    title={f.path}
                  >
                    <span class="git-badge">{f.indexStatus}{f.worktreeStatus}</span>
                    <span class="git-path">{f.path}</span>
                  </div>
                );
              })}

              <div class="section-label">Recent Commits</div>
              {commits.map((c) => (
                <div
                  class="git-commit-row"
                  onClick={() => onOpenCommitDiff(selectedRepo, c.hash)}
                  title={`${c.hash} — ${c.author}`}
                >
                  <span class="commit-sha">{c.shortHash}</span>
                  <span class="commit-msg">{c.subject}</span>
                </div>
              ))}
            </div>
          )}
        </div>
      )}
    </aside>
  );
}
