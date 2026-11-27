async function apiFetch(path, opts = {}) {
  const res = await fetch(path, {
    credentials: 'same-origin',
    headers: { 'Content-Type': 'application/json', ...(opts.headers || {}) },
    ...opts,
  });
  if (!res.ok) {
    const msg = await res.text();
    throw new Error(msg || `HTTP ${res.status}`);
  }
  return res.json();
}

export const api = {
  login: (token) => apiFetch('/api/v1/auth', { method: 'POST', body: JSON.stringify({ token }) }),
  projects: () => apiFetch('/api/v1/projects'),
  addProject: (path) => apiFetch('/api/v1/projects', { method: 'POST', body: JSON.stringify({ path }) }),
  sessions: (projectId) => apiFetch(`/api/v1/projects/${encodeURIComponent(projectId)}/sessions`),
  openSession: (payload) => apiFetch('/api/v1/sessions', { method: 'POST', body: JSON.stringify(payload) }),
  prompt: (id, text) => apiFetch(`/api/v1/sessions/${encodeURIComponent(id)}/prompt`, { method: 'POST', body: JSON.stringify({ text }) }),
  interrupt: (id, mode) => apiFetch(`/api/v1/sessions/${encodeURIComponent(id)}/interrupt`, { method: 'POST', body: JSON.stringify({ mode }) }),
};
