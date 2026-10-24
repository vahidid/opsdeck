import type { AgentEvent, RecallItem, TodoItem } from "../../api/types";

export type Ev<T extends AgentEvent["type"]> = Extract<AgentEvent, { type: T }>;

export type ApprovalItem = { req: Ev<"approval_request">; resolved?: Ev<"approval_resolved"> };

export type ToolItem = {
  kind: "tool";
  id: string;
  call?: Ev<"tool_call">;
  result?: Ev<"tool_result">;
  approvals: ApprovalItem[];
  hooks: Ev<"hook">[];
  question?: Ev<"question">;
  answer?: Ev<"question_answered">;
  todo?: TodoItem[];
  /** a delegate call: the subagent's own timeline */
  sub?: { start?: Ev<"subagent">; end?: Ev<"subagent">; items: Timeline[] };
};

export type Timeline =
  | { kind: "user"; id: string; text: string; command?: string; forkable: boolean }
  | { kind: "notification"; id: string; text: string }
  | { kind: "shell"; id: string; ev: Ev<"shell"> }
  | { kind: "recall"; id: string; items: RecallItem[]; preloaded?: string }
  | { kind: "assistant"; id: string; text: string; streaming: boolean }
  | ToolItem
  | { kind: "error"; id: string; message: string }
  | { kind: "compaction"; id: string; summary: string };

export type Block = Exclude<Timeline, { kind: "tool" }> | { kind: "tools"; id: string; items: ToolItem[] };

/**
 * callKey identifies a tool call in the chat: call ids are only unique
 * within one conversation, and a subagent has its own (local models often
 * number their calls call_0, call_1…).
 */
export function callKey(parent: string | undefined, callId: string): string {
  return parent ? `${parent}/${callId}` : callId;
}

/** consecutive tool calls are one group (like Claude Code) */
export function groupBlocks(items: Timeline[]): Block[] {
  const out: Block[] = [];
  for (const t of items) {
    if (t.kind === "tool") {
      const last = out[out.length - 1];
      if (last?.kind === "tools") last.items.push(t);
      else out.push({ kind: "tools", id: "g-" + t.id, items: [t] });
    } else out.push(t);
  }
  return out;
}

/**
 * buildTimeline turns the event list into what the chat shows. Events of a
 * subagent (with parent) go into the timeline of their delegate call.
 */
export function buildTimeline(events: AgentEvent[], deltas: Record<string, string>): { items: Timeline[]; todo?: TodoItem[] } {
  const root: Timeline[] = [];
  const tools = new Map<string, ToolItem>();
  const approvals = new Map<string, ToolItem>();
  let todo: TodoItem[] | undefined;

  const listFor = (parent?: string): Timeline[] => {
    if (!parent) return root;
    const p = toolFor(parent, undefined);
    p.sub ??= { items: [] };
    return p.sub.items;
  };
  function toolFor(callId: string, parent: string | undefined): ToolItem {
    const key = callKey(parent, callId);
    let t = tools.get(key);
    if (!t) {
      t = { kind: "tool", id: key, approvals: [], hooks: [] };
      tools.set(key, t);
      listFor(parent).push(t);
    }
    return t;
  }

  for (const e of events) {
    const parent = e.parent;
    switch (e.type) {
      case "user":
        root.push({ kind: "user", id: e.id, text: e.text, command: e.command, forkable: typeof e.msgIndex === "number" });
        break;
      case "notification":
        root.push({ kind: "notification", id: e.id, text: e.text });
        break;
      case "shell":
        root.push({ kind: "shell", id: e.id, ev: e });
        break;
      case "recall":
        if (e.items?.length) root.push({ kind: "recall", id: e.id, items: e.items, preloaded: e.preloaded });
        break;
      case "assistant":
        if (e.text?.trim()) listFor(parent).push({ kind: "assistant", id: e.messageId || e.id, text: e.text, streaming: false });
        break;
      case "assistant_delta":
        listFor(parent).push({ kind: "assistant", id: e.messageId, text: deltas[e.messageId] ?? "", streaming: true });
        break;
      case "tool_call":
        toolFor(e.callId, parent).call = e;
        break;
      case "tool_result":
        toolFor(e.callId, parent).result = e;
        break;
      case "approval_request": {
        const t = toolFor(e.callId, parent);
        t.approvals.push({ req: e });
        approvals.set(e.approvalId, t);
        break;
      }
      case "approval_resolved": {
        const t = approvals.get(e.approvalId);
        const a = t?.approvals.find((x) => x.req.approvalId === e.approvalId);
        if (a) a.resolved = e;
        break;
      }
      case "question":
        toolFor(e.callId, parent).question = e;
        break;
      case "question_answered":
        for (const t of tools.values()) if (t.question?.questionId === e.questionId) t.answer = e;
        break;
      case "todo":
        toolFor(e.callId, parent).todo = e.items;
        if (!parent) todo = e.items;
        break;
      case "hook":
        if (e.callId) toolFor(e.callId, parent).hooks.push(e);
        break;
      case "subagent": {
        const t = toolFor(e.callId, parent);
        t.sub ??= { items: [] };
        if (e.phase === "start") t.sub.start = e;
        else t.sub.end = e;
        break;
      }
      case "error":
        listFor(parent).push({ kind: "error", id: e.id, message: e.message });
        break;
      case "compaction":
        root.push({ kind: "compaction", id: e.id, summary: e.summary });
        break;
    }
  }
  return { items: root, todo };
}

/** approvals and questions that wait for the user, nested ones included */
export function waiting(items: Timeline[]): { approvals: number; questions: number } {
  let approvals = 0;
  let questions = 0;
  for (const t of items) {
    if (t.kind !== "tool") continue;
    approvals += t.approvals.filter((a) => !a.resolved).length;
    if (t.question && !t.answer && !t.result) questions++;
    if (t.sub) {
      const w = waiting(t.sub.items);
      approvals += w.approvals;
      questions += w.questions;
    }
  }
  return { approvals, questions };
}
