export type Project = { id: string; name: string };
export type Thread = { id: string; title: string };
export type Run = {
  id: string;
  goal?: string;
  status: string;
  plan_snapshot?: string;
  verification_snapshot?: string;
  artifact_paths?: string;
};
export type Message = { role: string; content: string };
export type Step = { id: string; title: string; status: string };
export type Finding = { criterion: string; status: string; evidence: string };
export type ToolLine = { kind: string; text: string };
export type MemoryItem = { kind: string; content: string };
export type Artifact = { path: string; exists: boolean };

export type RuntimeEvent = {
  type: string;
  payload: Record<string, unknown>;
};
