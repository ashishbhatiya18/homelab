export type SessionProfile = 'claude' | 'agy' | 'shell';

export interface SessionInfo {
  id: string;
  profile: SessionProfile;
  title: string;
  cwd: string;
  createdAt: string;
  clients: number;
  exited: boolean;
}

export interface FsEntry {
  name: string;
  path: string;
  isDir: boolean;
  size: number;
}

export interface FsListResponse {
  root: string;
  path: string;
  entries: FsEntry[];
}

export interface FsReadResponse {
  path: string;
  content: string;
  size: number;
  truncated: boolean;
  binary: boolean;
}

export interface GitRepo {
  name: string;
  path: string;
}

export interface GitFileStatus {
  path: string;
  indexStatus: string;
  worktreeStatus: string;
}

export interface GitStatusResponse {
  repo: string;
  branch: string;
  files: GitFileStatus[];
}

export interface GitCommit {
  hash: string;
  shortHash: string;
  subject: string;
  author: string;
  relativeDate: string;
}

export interface ServerConfig {
  workspaceRoot: string;
  allowFileWrite: boolean;
  allowOAuthRelay: boolean;
}
