import axios from 'axios';

// In production (embedded in Go binary) NEXT_PUBLIC_API_URL is "" → relative URLs.
// In dev it is unset (undefined) → falls back to localhost:8080.
const API_BASE_URL = process.env.NEXT_PUBLIC_API_URL ?? 'http://localhost:8080';

const api = axios.create({
  baseURL: API_BASE_URL,
  withCredentials: true,
});

export const stacksAPI = {
  getHealth: () =>
    api.get('/api/health'),
  listStacks: () =>
    api.get('/api/stacks'),

  // Stack-level operations — routes: /api/stacks/:node/:stack/...
  getStackStatus: (node: string, stack: string) =>
    api.get(`/api/stacks/${node}/${stack}`),
  startStack: (node: string, stack: string) =>
    api.post(`/api/stacks/${node}/${stack}/start`),
  stopStack: (node: string, stack: string) =>
    api.post(`/api/stacks/${node}/${stack}/stop`),
  restartStack: (node: string, stack: string) =>
    api.post(`/api/stacks/${node}/${stack}/restart`),
  rebuildStack: (node: string, stack: string) =>
    api.post(`/api/stacks/${node}/${stack}/rebuild`),
  updateStack: (node: string, stack: string) =>
    api.post(`/api/stacks/${node}/${stack}/update`),
  getLogs: (node: string, stack: string, lines: number = 100) =>
    api.get(`/api/stacks/${node}/${stack}/logs`, { params: { lines } }),

  // Container-level operations — routes: /api/stacks/:node/:stack/containers/:service/...
  startContainer: (node: string, stack: string, service: string) =>
    api.post(`/api/stacks/${node}/${stack}/containers/${service}/start`),
  stopContainer: (node: string, stack: string, service: string) =>
    api.post(`/api/stacks/${node}/${stack}/containers/${service}/stop`),
  restartContainer: (node: string, stack: string, service: string) =>
    api.post(`/api/stacks/${node}/${stack}/containers/${service}/restart`),
  getContainerLogs: (node: string, stack: string, service: string, lines: number = 100) =>
    api.get(`/api/stacks/${node}/${stack}/containers/${service}/logs`, { params: { lines } }),
};

export default api;
