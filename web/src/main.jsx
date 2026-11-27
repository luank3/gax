import { render } from 'preact';
import { useEffect, useRef, useState } from 'preact/hooks';
import { api } from './api.js';

function App() {
  const [authed, setAuthed] = useState(true);
  const [tokenInput, setTokenInput] = useState('');
  const [projects, setProjects] = useState([]);
  const [projectId, setProjectId] = useState('');
  const [sessions, setSessions] = useState([]);
  const [sessionId, setSessionId] = useState('');
  const [items, setItems] = useState([]);
  const [streamText, setStreamText] = useState('');
  const [status, setStatus] = useState('idle');
  const [meta, setMeta] = useState({ provider: '', model: '', input: 0, output: 0, cost: 0 });
  const [prompt, setPrompt] = useState('');
  const [newPath, setNewPath] = useState('');
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [err, setErr] = useState('');
  const promptRef = useRef(null);
  const esRef = useRef(null);

  const loadProjects = async () => {
    try {
      const list = await api.projects();
      setProjects(list || []);
      setAuthed(true);
      if (list && list.length > 0 && !projectId) {
        setProjectId(list[0].id);
      }
    } catch (e) {
      if (String(e.message).includes('401') || String(e.message).includes('unauthorized')) {
        setAuthed(false);
      } else {
        setErr(e.message);
      }
    }
  };

  useEffect(() => { loadProjects(); }, []);

  useEffect(() => {
    if (!projectId) return;
    api.sessions(projectId).then((list) => setSessions(list || [])).catch((e) => setErr(e.message));
  }, [projectId]);

  useEffect(() => {
    if (esRef.current) { esRef.current.close(); esRef.current = null; }
    if (!sessionId) return;
    const es = new EventSource(`/api/v1/sessions/${encodeURIComponent(sessionId)}/events`);
    esRef.current = es;
    es.addEventListener('snapshot', (ev) => {
      const data = JSON.parse(ev.data);
      setItems(data.items || []);
      setStreamText('');
      setStatus(data.running ? 'running' : 'idle');
      const tot = data.totals || {};
      setMeta({ provider: data.provider || '', model: data.model || '', input: tot.InputTokens || 0, output: tot.OutputTokens || 0, cost: tot.CostTotal || 0 });
    });
    es.addEventListener('agent', (ev) => {
      const data = JSON.parse(ev.data);
      if (data.type === 'text_delta') {
        setStreamText((t) => t + data.text);
      } else if (data.type === 'turn_started') {
        setStatus('running');
      } else if (data.type === 'tool_started') {
        setStatus(`tool:${data.name}`);
      } else if (data.type === 'turn_usage') {
        setMeta((m) => ({ ...m, input: m.input + (data.input || 0), output: m.output + (data.output || 0), cost: m.cost + (data.cost || 0) }));
      } else if (data.type === 'turn_finished') {
        setStatus(data.outcome || 'idle');
        setStreamText((cur) => {
          if (cur) setItems((prev) => [...prev, { kind: 'assistant', text: cur }]);
          return '';
        });
      }
    });
    return () => es.close();
  }, [sessionId]);

  useEffect(() => {
    let lastEsc = 0;
    const onKey = (e) => {
      if ((e.ctrlKey || e.metaKey) && e.key === 'k') {
        e.preventDefault();
        setDrawerOpen((v) => !v);
      } else if (e.key === 'Escape' && sessionId && status !== 'idle') {
        const now = Date.now();
        api.interrupt(sessionId, now - lastEsc < 800 ? 'abort' : 'pause').catch(() => {});
        lastEsc = now;
      }
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [sessionId, status]);

  if (!authed) {
    return (
      <div class="min-h-screen flex items-center justify-center p-4">
        <form class="w-full max-w-sm border border-[var(--border)] bg-[var(--bg-surface)] p-4 rounded space-y-3"
          onSubmit={async (e) => {
            e.preventDefault();
            try { await api.login(tokenInput.trim()); setTokenInput(''); await loadProjects(); }
            catch (ex) { setErr(ex.message); }
          }}>
          <div class="font-semibold">gax serve authentication</div>
          <input type="password" value={tokenInput} onInput={(e) => setTokenInput(e.currentTarget.value)}
            placeholder="Bearer token" class="w-full px-2 py-1.5 rounded border border-[var(--border)] bg-[var(--bg-base)]" />
          {err && <div class="text-xs text-[var(--danger)]">{err}</div>}
          <button type="submit" class="w-full py-1.5 rounded bg-[var(--accent)] text-white">Sign in</button>
        </form>
      </div>
    );
  }

  const startSession = async (opts = {}) => {
    if (!projectId) return;
    const res = await api.openSession({ project_id: projectId, ...opts });
    setSessionId(res.id);
    setDrawerOpen(false);
    const list = await api.sessions(projectId);
    setSessions(list || []);
  };

  const sendPrompt = async (e) => {
    e.preventDefault();
    const text = prompt.trim();
    if (!text || !sessionId) return;
    setPrompt('');
    setItems((prev) => [...prev, { kind: 'user', text }]);
    try { await api.prompt(sessionId, text); }
    catch (ex) { setErr(ex.message); }
  };

  return (
    <div class="h-screen flex flex-col md:flex-row overflow-hidden">
      <aside class={`${drawerOpen ? 'block' : 'hidden'} md:block w-full md:w-64 border-b md:border-b-0 md:border-r border-[var(--border)] bg-[var(--bg-surface)] p-2 overflow-y-auto shrink-0`}>
        <div class="text-[11px] uppercase tracking-wider text-[var(--text-muted)] px-1 mb-1">Projects</div>
        <form class="flex gap-1 mb-2" onSubmit={async (e) => {
          e.preventDefault();
          if (!newPath.trim()) return;
          try { const p = await api.addProject(newPath.trim()); setNewPath(''); setProjectId(p.id); await loadProjects(); }
          catch (ex) { setErr(ex.message); }
        }}>
          <input value={newPath} onInput={(e) => setNewPath(e.currentTarget.value)} placeholder="/path/to/repo"
            class="flex-1 min-w-0 px-2 py-1 text-xs rounded border border-[var(--border)] bg-[var(--bg-base)]" />
          <button type="submit" class="px-2 py-1 text-xs rounded bg-[var(--bg-elevated)]">+</button>
        </form>
        <div class="space-y-0.5 mb-3">
          {projects.map((p) => (
            <button key={p.id} onClick={() => setProjectId(p.id)}
              class={`w-full text-left px-2 py-1 rounded truncate ${projectId === p.id ? 'bg-[var(--bg-elevated)] text-[var(--accent)]' : 'hover:bg-[var(--bg-elevated)]'}`}>
              {p.name}
            </button>
          ))}
        </div>
        <div class="flex items-center justify-between px-1 mb-1">
          <span class="text-[11px] uppercase tracking-wider text-[var(--text-muted)]">Sessions</span>
          <button onClick={() => startSession()} class="text-xs text-[var(--accent)]">+ new</button>
        </div>
        <div class="space-y-0.5">
          {sessions.map((s) => (
            <div key={s.id} class="flex items-center gap-1">
              <button onClick={() => startSession({ resume_id: s.id })}
                class={`flex-1 min-w-0 text-left px-2 py-1 rounded truncate text-xs ${sessionId === s.id ? 'bg-[var(--bg-elevated)]' : 'hover:bg-[var(--bg-elevated)]'}`}>
                {s.preview || s.id.slice(0, 8)}
              </button>
              <button title="Fork" onClick={() => startSession({ fork_from: s.id })} class="px-1 text-[11px] text-[var(--text-muted)]">fork</button>
            </div>
          ))}
        </div>
      </aside>
      <main class="flex-1 flex flex-col min-w-0">
        <header class="border-b border-[var(--border)] px-3 py-1.5 flex items-center justify-between gap-2 bg-[var(--bg-surface)]">
          <div class="flex items-center gap-2 min-w-0">
            <button onClick={() => setDrawerOpen((v) => !v)} class="md:hidden px-1.5 py-0.5 border border-[var(--border)] rounded text-xs">menu</button>
            <span class="truncate font-semibold">{sessionId ? sessionId.slice(0, 8) : 'select or create session'}</span>
          </div>
          <div class="flex items-center gap-2 text-xs tabular-nums">
            {meta.provider && <span class="text-[var(--text-muted)] hidden sm:inline">{meta.provider}{meta.model ? `/${meta.model}` : ''} · in:{meta.input} out:{meta.output} ${meta.cost.toFixed(4)}</span>}
            <span class="text-[var(--text-muted)]">{status}</span>
            {sessionId && status !== 'running' && items.length > 0 && (
              <button onClick={async () => { const r = await api.undo(sessionId); setItems(r.items || []); }} class="px-2 py-0.5 rounded border border-[var(--border)]">undo</button>
            )}
            {sessionId && status === 'running' && (
              <button onClick={() => api.interrupt(sessionId, 'pause')} class="px-2 py-0.5 rounded border border-[var(--border)]">pause</button>
            )}
          </div>
        </header>
        {err && <div class="px-3 py-1 text-xs text-[var(--danger)] border-b border-[var(--border)]">{err}</div>}
        <div class="flex-1 overflow-y-auto p-3 space-y-2">
          {items.map((it, i) => (
            it.kind === 'tool_call' || it.kind === 'tool_result' ? (
              <details key={i} class="border border-[var(--border)] rounded px-2 py-1 text-xs bg-[var(--bg-surface)]">
                <summary class="cursor-pointer text-[var(--text-muted)]">{it.kind}{it.tool_name ? `:${it.tool_name}` : ''}</summary>
                <pre class="whitespace-pre-wrap break-words mt-1 mb-0">{it.output || it.arguments}</pre>
              </details>
            ) : (
              <div key={i} class={`p-2 rounded border ${it.kind === 'user' ? 'border-[var(--border)] bg-[var(--bg-surface)]' : 'border-transparent'}`}>
                <div class="text-[10px] uppercase text-[var(--text-muted)]">{it.kind}</div>
                <pre class="whitespace-pre-wrap break-words m-0">{it.text}</pre>
              </div>
            )
          ))}
          {streamText && (
            <div class="p-2">
              <div class="text-[10px] uppercase text-[var(--accent)]">assistant</div>
              <pre class="whitespace-pre-wrap break-words m-0">{streamText}</pre>
            </div>
          )}
        </div>
        <form class="border-t border-[var(--border)] p-2 flex gap-2 bg-[var(--bg-surface)]" onSubmit={sendPrompt}>
          <textarea ref={promptRef} rows="2" value={prompt} onInput={(e) => setPrompt(e.currentTarget.value)}
            onKeyDown={(e) => { if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); sendPrompt(e); } }}
            placeholder={sessionId ? 'Prompt (Enter to send, Shift+Enter newline, Esc pause)...' : 'Select or create a session first'}
            disabled={!sessionId}
            class="flex-1 px-2 py-1 rounded border border-[var(--border)] bg-[var(--bg-base)] resize-none" />
          <button type="submit" disabled={!sessionId} class="px-3 py-1 rounded bg-[var(--accent)] text-white disabled:opacity-40">Send</button>
        </form>
      </main>
    </div>
  );
}

render(<App />, document.getElementById('app'));
