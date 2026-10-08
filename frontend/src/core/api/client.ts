async function send(path: string, init?: RequestInit) {
  const response = await fetch(path, {
    ...init,
    headers: { "Content-Type": "application/json", ...(init?.headers ?? {}) },
  });
  const text = await response.text();
  const body = text ? JSON.parse(text) : null;
  if (!response.ok) {
    throw new Error(body?.error || body?.detail || response.statusText);
  }
  return body;
}

export const api = {
  projects: () => send("/api/projects") as Promise<{ id: string; name: string }[]>,
  createProject: (name: string) => send("/api/projects", { method: "POST", body: JSON.stringify({ name }) }),
  threads: (projectID: string) => send(`/api/projects/${projectID}/threads`),
  createThread: (projectID: string, title: string) =>
    send(`/api/projects/${projectID}/threads`, { method: "POST", body: JSON.stringify({ title }) }),
  messages: (threadID: string) => send(`/api/threads/${threadID}/messages`),
  runs: (threadID: string) => send(`/api/threads/${threadID}/runs`),
  run: (runID: string) => send(`/api/runs/${runID}`),
  createRun: (threadID: string, projectID: string, goal: string) =>
    send(`/api/threads/${threadID}/runs?project_id=${projectID}`, { method: "POST", body: JSON.stringify({ goal }) }),
  cancel: (runID: string) => send(`/api/runs/${runID}/cancel`, { method: "POST", body: "{}" }),
  resume: (runID: string) => send(`/api/runs/${runID}/resume`, { method: "POST", body: "{}" }),
  artifacts: (runID: string) => send(`/api/runs/${runID}/artifacts`),
  file: async (runID: string, path: string) => {
    const response = await fetch(`/api/runs/${runID}/files/${path}`);
    if (!response.ok) throw new Error("无法读取文件");
    return response.text();
  },
  memory: (projectID: string) => send(`/api/projects/${projectID}/memory`),
};
