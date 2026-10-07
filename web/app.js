const view = {
  projectId: "",
  threadId: "",
  runId: "",
  status: "尚未开始",
  messages: [],
  plan: [],
  verification: null,
  subagents: [],
  artifacts: [],
  timeline: [],
};

const $ = (id) => document.getElementById(id);

async function api(path, options) {
  const response = await fetch(path, options);
  if (!response.ok) throw new Error(await response.text());
  return response.json();
}

function render() {
  $("run-status").textContent = view.status;
  $("message-list").innerHTML = view.messages.map((item) => `<li class="${item.role}">${escape(item.content)}</li>`).join("");
  $("plan").innerHTML = view.plan.map((step) => `<li>${escape(step.id)} ${escape(step.title)} — ${escape(step.status)}</li>`).join("");
  $("verification").textContent = view.verification ? JSON.stringify(view.verification, null, 2) : "无";
  $("subagents").innerHTML = view.subagents.map((item) => `<li>${escape(item.id)} ${escape(item.status)} ${escape(item.summary || item.task || "")}</li>`).join("");
  $("artifacts").innerHTML = view.artifacts.map((path) => `<li>${escape(path)}</li>`).join("");
  $("timeline").innerHTML = view.timeline.map((item) => `<li>${item.seq} ${escape(item.type)}</li>`).join("");
}

function applyEvent(event) {
  view.timeline.push({ seq: event.seq, type: event.type });
  const payload = event.payload || {};
  if (event.type === "model.message") view.messages.push({ role: "assistant", content: payload.content || "" });
  if (event.type === "tool.completed") view.messages.push({ role: "tool", content: `${payload.tool} ${payload.summary || ""}` });
  if (event.type === "plan.updated") view.plan = payload.plan?.steps || [];
  if (event.type === "verification.completed") view.verification = payload;
  if (event.type === "replan.started") view.timeline[view.timeline.length - 1].type = `replan.started ${payload.reason || ""}`;
  if (event.type === "subagent.started") view.subagents.push({ id: payload.subagent_id, status: "running", task: payload.task });
  if (event.type === "subagent.completed") {
    const found = view.subagents.find((item) => item.id === payload.subagent_id);
    if (found) {
      found.status = payload.status;
      found.summary = payload.summary;
    }
  }
  if (event.type === "run.completed") {
    view.status = "completed";
    view.artifacts = payload.artifacts || [];
  }
  if (event.type === "run.failed") view.status = "failed";
  if (event.type === "run.cancelled") view.status = "cancelled";
  if (event.type === "run.started") view.status = "running";
  render();
}

function escape(value) {
  return String(value ?? "").replace(/[&<>"]/g, (ch) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" }[ch]));
}

async function refreshProjects() {
  const projects = await api("/api/projects");
  $("project-list").innerHTML = projects.map((item) => `<li><button data-project="${item.id}">${escape(item.name)}</button></li>`).join("");
}

async function refreshThreads() {
  if (!view.projectId) return;
  const threads = await api(`/api/projects/${view.projectId}/threads`);
  $("thread-list").innerHTML = threads.map((item) => `<li><button data-thread="${item.id}">${escape(item.title)}</button></li>`).join("");
}

$("project-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  const name = new FormData(event.target).get("name");
  await api("/api/projects", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ name }) });
  event.target.reset();
  await refreshProjects();
});

$("thread-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  if (!view.projectId) return;
  const title = new FormData(event.target).get("title");
  await api(`/api/projects/${view.projectId}/threads`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ title }) });
  event.target.reset();
  await refreshThreads();
});

$("project-list").addEventListener("click", async (event) => {
  const id = event.target.dataset.project;
  if (!id) return;
  view.projectId = id;
  await refreshThreads();
});

$("thread-list").addEventListener("click", async (event) => {
  const id = event.target.dataset.thread;
  if (!id) return;
  view.threadId = id;
  view.messages = await api(`/api/threads/${id}/messages`);
  render();
});

$("goal-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  if (!view.threadId || !view.projectId) return;
  const goal = new FormData(event.target).get("goal");
  const run = await api(`/api/threads/${view.threadId}/runs?project_id=${view.projectId}`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ goal }),
  });
  view.runId = run.id;
  view.status = "running";
  view.messages.push({ role: "user", content: goal });
  view.plan = [];
  view.verification = null;
  view.subagents = [];
  view.artifacts = [];
  view.timeline = [];
  render();
  const source = new EventSource(`/api/runs/${run.id}/events`);
  source.onmessage = (message) => applyEvent(JSON.parse(message.data));
  source.addEventListener("run.started", (message) => applyEvent(JSON.parse(message.data)));
  ["model.message", "tool.completed", "plan.updated", "verification.completed", "replan.started", "subagent.started", "subagent.completed", "run.completed", "run.failed", "run.cancelled", "tool.started", "memory.written"].forEach((type) => {
    source.addEventListener(type, (message) => applyEvent(JSON.parse(message.data)));
  });
});

refreshProjects().catch((error) => { $("run-status").textContent = error.message; });
