import { Plus, Trash2, Webhook } from "lucide-react";
import type { HookDef } from "../api/types";
import { Badge, Button, ChipInput, FormField, IconButton, Input, Select, Toggle, cx } from "./ui";

const EVENTS: { id: HookDef["event"]; label: string; help: string }[] = [
  { id: "before_tool", label: "Before a tool call", help: "Exit 2 or print {\"decision\":\"deny\",\"reason\":\"…\"} to block the call; {\"decision\":\"ask\"} makes it ask." },
  { id: "after_tool", label: "After a tool call", help: "Gets the result too; for logs and notifications. Does not hold up the agent." },
  { id: "turn_end", label: "When a turn ends", help: "Gets the agent's last message; e.g. post it to a chat channel." },
];

/**
 * HooksEditor edits hook commands: they run on this computer with the event
 * as JSON on stdin (workspace, session, tool, input, risk, target…).
 */
export function HooksEditor({
  hooks,
  onChange,
  untrusted = [],
  canAllow,
  environments = [],
  workspaces,
}: {
  hooks: HookDef[];
  onChange: (h: HookDef[]) => void;
  untrusted?: number[];
  /** App settings hooks may also let an asking call run */
  canAllow?: boolean;
  environments?: string[];
  workspaces?: string[];
}) {
  const set = (i: number, h: Partial<HookDef>) => onChange(hooks.map((x, j) => (j === i ? { ...x, ...h } : x)));
  return (
    <div className="space-y-3">
      {hooks.length === 0 && <div className="text-xs text-faint">No hooks.</div>}
      {hooks.map((h, i) => {
        const ev = EVENTS.find((e) => e.id === h.event) ?? EVENTS[0];
        const bad = untrusted.includes(i);
        return (
          <div key={i} className={cx("rounded-lg border bg-panel-2 p-3 space-y-2.5", bad ? "border-warn/50" : "border-line")}>
            <div className="flex items-center gap-2">
              <Webhook className="size-3.5 text-muted" />
              <Input value={h.name} onChange={(e) => set(i, { name: e.target.value })} placeholder="name" className="w-48" />
              <Select value={h.event} onChange={(e) => set(i, { event: e.target.value as HookDef["event"] })} className="w-48">
                {EVENTS.map((e) => (
                  <option key={e.id} value={e.id}>
                    {e.label}
                  </option>
                ))}
              </Select>
              {bad && <Badge tone="warn">not trusted on this machine</Badge>}
              <div className="flex-1" />
              <Toggle checked={!h.disabled} onChange={(v) => set(i, { disabled: !v })} label="on" />
              <IconButton title="Remove hook" onClick={() => onChange(hooks.filter((_, j) => j !== i))}>
                <Trash2 className="size-3.5" />
              </IconButton>
            </div>
            <FormField label="Command" help={ev.help + (canAllow && h.event === "before_tool" ? " {\"decision\":\"allow\"} lets a call that would ask run." : "")}>
              <Input mono value={h.command} onChange={(e) => set(i, { command: e.target.value })} placeholder={h.event === "turn_end" ? "curl -sS -d @- https://hooks.slack.com/…" : "~/bin/opsdeck-policy"} />
            </FormField>
            {h.event !== "turn_end" && (
              <div className="grid grid-cols-[1fr_140px_1fr] gap-2">
                <FormField label="Tools" help="* for any; e.g. exec, local_*">
                  <Input mono value={h.tool ?? ""} onChange={(e) => set(i, { tool: e.target.value })} placeholder="any tool" />
                </FormField>
                <FormField label="Risk">
                  <Select value={h.risk ?? ""} onChange={(e) => set(i, { risk: e.target.value as HookDef["risk"] })}>
                    <option value="">any</option>
                    <option value="read">read</option>
                    <option value="change">change</option>
                    <option value="destructive">destructive</option>
                  </Select>
                </FormField>
                <FormField label="Environments">
                  <ChipInput mono value={h.environment ?? []} onChange={(v) => set(i, { environment: v })} placeholder={environments[0] ?? "prod"} />
                </FormField>
              </div>
            )}
            {workspaces && (
              <FormField label="Workspace">
                <Select value={h.workspace ?? ""} onChange={(e) => set(i, { workspace: e.target.value })}>
                  <option value="">every workspace</option>
                  {workspaces.map((w) => (
                    <option key={w} value={w}>
                      {w}
                    </option>
                  ))}
                </Select>
              </FormField>
            )}
          </div>
        );
      })}
      <Button size="xs" variant="ghost" icon={<Plus className="size-3" />} onClick={() => onChange([...hooks, { name: "", event: "before_tool", command: "" }])}>
        Add hook
      </Button>
    </div>
  );
}
