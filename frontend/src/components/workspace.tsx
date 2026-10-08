"use client";

import { useEffect, useMemo, useState } from "react";

import { api } from "@/core/api/client";
import type { Artifact, Finding, MemoryItem, Message, Project, Run, Step, Thread, ToolLine } from "@/types/runtime";

const terminal = new Set(["run.completed", "run.failed", "run.cancelled"]);

function stepsFrom(raw: string | undefined): Step[] {
  if (!raw) return [];
  try {
    const body = JSON.parse(raw);
    return body.plan?.steps ?? body.steps ?? [];
  } catch {
    return [];
  }
}

function findingsFrom(raw: string | undefined): { passed: boolean | null; findings: Finding[] } {
  if (!raw) return { passed: null, findings: [] };
  try {
    const body = JSON.parse(raw);
    return { passed: Boolean(body.passed), findings: body.findings ?? [] };
  } catch {
    return { passed: null, findings: [] };
  }
}

export function Workspace() {
  const [projects, setProjects] = useState<Project[]>([]);
  const [threads, setThreads] = useState<Thread[]>([]);
  const [projectID, setProjectID] = useState("");
  const [threadID, setThreadID] = useState("");
  const [messages, setMessages] = useState<Message[]>([]);
  const [runs, setRuns] = useState<Run[]>([]);
  const [run, setRun] = useState<Run | null>(null);
  const [steps, setSteps] = useState<Step[]>([]);
  const [verification, setVerification] = useState<{ passed: boolean | null; findings: Finding[] }>({ passed: null, findings: [] });
  const [replan, setReplan] = useState("");
  const [tools, setTools] = useState<ToolLine[]>([]);
  const [artifacts, setArtifacts] = useState<Artifact[]>([]);
  const [file, setFile] = useState("");
  const [memory, setMemory] = useState<MemoryItem[]>([]);
  const [goal, setGoal] = useState("");
  const [name, setName] = useState("");
  const [title, setTitle] = useState("");
  const [error, setError] = useState("");
  const [waiting, setWaiting] = useState(false);

  async function loadProjects() {
    const rows = await api.projects();
    setProjects(rows);
    if (!projectID && rows[0]) setProjectID(rows[0].id);
  }

  useEffect(() => {
    loadProjects().catch((err: Error) => setError(err.message));
  }, []);

  useEffect(() => {
    if (!projectID) return;
    api.threads(projectID).then((rows: Thread[]) => {
      setThreads(rows);
      setThreadID(rows[0]?.id ?? "");
    }).catch((err: Error) => setError(err.message));
    api.memory(projectID).then(setMemory).catch(() => setMemory([]));
  }, [projectID]);

  useEffect(() => {
    if (!threadID) return;
    api.messages(threadID).then(setMessages).catch((err: Error) => setError(err.message));
    api.runs(threadID).then((rows: Run[]) => {
      setRuns(rows);
      if (rows[0]) selectRun(rows[0].id);
    }).catch((err: Error) => setError(err.message));
  }, [threadID]);

  function applyEvent(type: string, payload: Record<string, unknown>) {
    if (type === "plan.updated") {
      const plan = payload.plan as { steps?: Step[] } | undefined;
      setSteps(plan?.steps ?? []);
    }
    if (type === "verification.completed") {
      setVerification({ passed: Boolean(payload.passed), findings: (payload.findings as Finding[]) ?? [] });
    }
    if (type === "replan.started") setReplan(String(payload.reason ?? "verification_failed"));
    if (type === "tool.started" || type === "tool.completed" || type === "subagent.started" || type === "subagent.completed") {
      const text = String(payload.tool ?? payload.task ?? payload.summary ?? "");
      setTools((items) => [...items, { kind: type, text }]);
    }
    if (type === "model.message") {
      setMessages((items) => [...items, { role: "assistant", content: String(payload.content ?? "") }]);
    }
    if (type === "memory.written") {
      setMemory((items) => [...items, { kind: String(payload.kind ?? ""), content: String(payload.summary ?? "") }]);
    }
    if (type === "run.completed" || type === "run.failed" || type === "run.cancelled") {
      const status = type.replace("run.", "");
      setRun((current) => (current ? { ...current, status } : current));
      setWaiting(false);
    }
  }

  function watch(runID: string) {
    setWaiting(true);
    const source = new EventSource(`/api/runs/${runID}/events?after_seq=0`);
    const types = ["run.started", "plan.updated", "model.message", "tool.started", "tool.completed", "subagent.started", "subagent.completed", "verification.completed", "replan.started", "memory.written", "run.completed", "run.failed", "run.cancelled"];
    for (const type of types) {
      source.addEventListener(type, (message) => {
        const event = JSON.parse((message as MessageEvent).data);
        applyEvent(event.type, event.payload ?? {});
        if (terminal.has(event.type)) {
          source.close();
          api.artifacts(runID).then(setArtifacts).catch(() => undefined);
          api.messages(threadID).then(setMessages).catch(() => undefined);
        }
      });
    }
    source.onerror = () => {
      if (source.readyState === EventSource.CLOSED) setWaiting(false);
    };
  }

  async function selectRun(runID: string) {
    const current = await api.run(runID);
    setRun(current);
    setSteps(stepsFrom(current.plan_snapshot));
    setVerification(findingsFrom(current.verification_snapshot));
    setArtifacts(await api.artifacts(runID));
    setFile("");
    setTools([]);
    setReplan("");
    if (current.status === "running" || current.status === "cancel_requested") watch(runID);
  }

  async function submit() {
    if (!goal.trim() || !projectID || !threadID) return;
    setError("");
    setMessages((items) => [...items, { role: "user", content: goal }]);
    const created = await api.createRun(threadID, projectID, goal);
    setGoal("");
    setRun(created);
    setSteps([]);
    setVerification({ passed: null, findings: [] });
    setTools([]);
    setReplan("");
    setArtifacts([]);
    watch(created.id);
  }

  const statusLabel = useMemo(() => {
    const status = run?.status;
    if (status === "completed") return "已完成";
    if (status === "failed") return "失败";
    if (status === "cancelled") return "已取消";
    if (status === "running" || waiting) return "运行中";
    return "尚无 Run";
  }, [run, waiting]);

  return (
    <div className="shell">
      <aside className="side">
        <div className="brand">Cakerdesk</div>
        <div className="muted">Project</div>
        {projects.map((item) => (
          <button key={item.id} className={item.id === projectID ? "item active" : "item"} onClick={() => setProjectID(item.id)}>
            {item.name}
          </button>
        ))}
        <div className="row">
          <input value={name} placeholder="新项目" onChange={(event) => setName(event.target.value)} />
          <button className="ghost" onClick={() => api.createProject(name).then(() => { setName(""); return loadProjects(); })}>创建</button>
        </div>
        <div className="muted">Thread</div>
        {threads.map((item) => (
          <button key={item.id} className={item.id === threadID ? "item active" : "item"} onClick={() => setThreadID(item.id)}>
            {item.title}
          </button>
        ))}
        <div className="row">
          <input value={title} placeholder="新线程" onChange={(event) => setTitle(event.target.value)} />
          <button className="ghost" onClick={() => projectID && api.createThread(projectID, title).then((row: Thread) => { setTitle(""); setThreads((items) => [...items, row]); setThreadID(row.id); })}>创建</button>
        </div>
        {runs[0] ? <div className="muted">最近任务：{runs[0].status}</div> : null}
      </aside>
      <main className="main">
        <div className="messages">
          {messages.map((item, index) => (
            <div key={index} className={item.role === "user" ? "bubble user" : "bubble"}>{item.content}</div>
          ))}
          {error ? <div className="bad">{error}</div> : null}
        </div>
        <form className="composer" onSubmit={(event) => { event.preventDefault(); submit().catch((err: Error) => setError(err.message)); }}>
          <textarea value={goal} placeholder="把任务交给这个线程" onChange={(event) => setGoal(event.target.value)} />
          <button className="solid" type="submit">运行</button>
        </form>
      </main>
      <aside className="run">
        <h1 className="status">{statusLabel}</h1>
        <div className="row">
          <button className="ghost" disabled={!run || !waiting} onClick={() => run && api.cancel(run.id).catch((err: Error) => setError(err.message))}>取消</button>
          <button className="ghost" disabled={run?.status !== "failed"} onClick={() => run && api.resume(run.id).then(() => watch(run.id)).catch((err: Error) => setError(err.message))}>恢复</button>
        </div>
        <section className="section">
          <h2>Plan</h2>
          {steps.map((step) => (
            <div key={step.id} className="step">{step.id} {step.title} <span className={step.status === "completed" ? "ok" : step.status === "blocked" ? "bad" : ""}>{step.status}</span></div>
          ))}
        </section>
        <section className="section">
          <h2>执行</h2>
          {tools.map((item, index) => <div key={index} className="log">{item.kind} {item.text}</div>)}
        </section>
        <section className="section">
          <h2>Verification</h2>
          {verification.passed === null ? <div className="muted">还没有验收</div> : <div className={verification.passed ? "ok" : "bad"}>{verification.passed ? "通过" : "未通过"}</div>}
          {verification.findings.filter((item) => item.status === "failed").map((item, index) => (
            <div key={index} className="bad">{item.criterion} {item.evidence}</div>
          ))}
        </section>
        <section className="section">
          <h2>Replan</h2>
          <div>{replan || "尚未重规划"}</div>
        </section>
        <section className="section">
          <h2>Artifacts</h2>
          {artifacts.map((item) => (
            <button key={item.path} className="file" onClick={() => run && api.file(run.id, item.path).then(setFile)}>{item.path}</button>
          ))}
          {file ? <pre className="filebody">{file}</pre> : null}
        </section>
        <section className="section">
          <h2>Memory</h2>
          {memory.length === 0 ? <div className="muted">这个项目还没有记忆</div> : memory.map((item, index) => (
            <div key={index} className="log">{item.kind}: {item.content}</div>
          ))}
        </section>
      </aside>
    </div>
  );
}
