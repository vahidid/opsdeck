import { useEffect, useState } from "react";
import { useSearchParams } from "react-router-dom";
import { CheckCheck, ListChecks, ListTree, Play, Tags } from "lucide-react";
import { api, errMsg } from "../../api/client";
import type { AnsibleRun, AnsibleRunKind, AnsibleRunOptions } from "../../api/types";
import { Modal, useToast } from "../../components/overlay";
import { Badge, Button, CodeBlock, EmptyState, FormField, Input, Segmented, Select, Textarea, Toggle, cx } from "../../components/ui";
import { useLocalStorage } from "../../hooks";
import { RunView } from "./RunView";
import { useAnsible } from "./shared";

type Form = Required<Pick<AnsibleRunOptions, "kind">> & AnsibleRunOptions;

const EMPTY: Form = { kind: "playbook", check: false, diff: false, verbosity: 0, extraVars: "", module: "ansible.builtin.ping", pattern: "all" };

export function inventoryLabel(inv: string): string {
  return inv === "workspace" ? "Workspace hosts (generated)" : inv;
}

// RunTab: what to run on the left, the run's live log on the right.
export function RunTab() {
  const { ws, project: p, withPassword, reload } = useAnsible();
  const toast = useToast();
  const [params, setParams] = useSearchParams();
  const [form, setForm] = useLocalStorage<Form>(`opsdeck.ansible.run.${ws}.${p.name}`, { ...EMPTY, playbook: p.playbooks[0] });
  const set = (patch: Partial<Form>) => setForm({ ...form, ...patch });
  const [busy, setBusy] = useState(false);
  const [check, setCheck] = useState<{ title: string; output: string; ok: boolean } | null>(null);
  const [checking, setChecking] = useState("");
  const runId = params.get("run") ?? "";

  // a playbook chosen elsewhere (Files → Run)
  const wanted = params.get("playbook");
  useEffect(() => {
    if (wanted && (wanted !== form.playbook || form.kind !== "playbook")) setForm({ ...form, kind: "playbook", playbook: wanted });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [wanted]);

  const setRun = (id: string) =>
    setParams(
      (prev) => {
        const n = new URLSearchParams(prev);
        n.set("run", id);
        n.delete("playbook");
        return n;
      },
      { replace: true },
    );

  async function start() {
    setBusy(true);
    try {
      const opts: AnsibleRunOptions = { ...form };
      if (opts.kind !== "playbook") delete opts.playbook;
      const r = await withPassword((pw) => api.ansibleRun(ws, p.name, { ...opts, password: pw?.password, vaultId: pw?.vaultId }));
      if (r) {
        setRun(r.id);
        void reload();
      }
    } catch (e) {
      toast.error("Could not start", errMsg(e));
    } finally {
      setBusy(false);
    }
  }

  async function quick(kind: "syntax" | "list-hosts" | "list-tasks" | "list-tags", title: string) {
    if (!form.playbook) return;
    setChecking(kind);
    try {
      const r = await withPassword(() => api.ansibleCheck(ws, p.name, { kind, playbook: form.playbook!, inventory: form.inventory, limit: form.limit, tags: form.tags }));
      if (r) setCheck({ title, output: r.output || r.error || "", ok: r.ok });
    } catch (e) {
      toast.error(title + " failed", errMsg(e));
    } finally {
      setChecking("");
    }
  }

  function rerun(r: AnsibleRun) {
    setForm({ ...EMPTY, ...r.options });
    toast.info("Options copied", "Check them and start the run.");
  }

  const locked = p.vaultIds.filter((id) => !p.unlocked.includes(id));
  const inventories = Array.from(new Set([...(p.inventory ? [p.inventory] : []), ...p.inventories, "workspace"]));
  const kind = form.kind;
  const canStart = kind === "playbook" ? !!form.playbook : kind === "galaxy" ? p.requirements.length > 0 : true;

  return (
    <div className="flex h-full min-h-0 gap-4">
      <aside className="w-[360px] shrink-0 overflow-y-auto pr-1 space-y-3">
        <Segmented<AnsibleRunKind>
          value={kind}
          onChange={(k) => set({ kind: k })}
          options={[
            { id: "playbook", label: "Playbook" },
            { id: "adhoc", label: "Ad-hoc", title: "One module on a host pattern" },
            { id: "galaxy", label: "Galaxy", title: "Install roles and collections" },
            { id: "lint", label: "Lint", title: "ansible-lint", },
          ]}
        />
        {kind === "playbook" && (
          <FormField label="Playbook">
            <Select value={form.playbook ?? ""} onChange={(e) => set({ playbook: e.target.value })}>
              {p.playbooks.length === 0 && <option value="">No playbooks found</option>}
              {p.playbooks.map((pb) => (
                <option key={pb} value={pb}>
                  {pb}
                </option>
              ))}
            </Select>
          </FormField>
        )}
        {kind === "adhoc" && (
          <>
            <div className="grid grid-cols-2 gap-2">
              <FormField label="Hosts">
                <Input mono value={form.pattern ?? ""} onChange={(e) => set({ pattern: e.target.value })} placeholder="all" />
              </FormField>
              <FormField label="Module">
                <Input mono value={form.module ?? ""} onChange={(e) => set({ module: e.target.value })} placeholder="ansible.builtin.ping" list="ansible-modules" />
              </FormField>
            </div>
            <datalist id="ansible-modules">
              {["ansible.builtin.ping", "ansible.builtin.setup", "ansible.builtin.command", "ansible.builtin.shell", "ansible.builtin.service", "ansible.builtin.apt", "ansible.builtin.dnf", "ansible.builtin.copy", "ansible.builtin.reboot"].map((m) => (
                <option key={m} value={m} />
              ))}
            </datalist>
            <FormField label="Arguments">
              <Input mono value={form.args ?? ""} onChange={(e) => set({ args: e.target.value })} placeholder="uptime   ·   name=nginx state=restarted" />
            </FormField>
          </>
        )}
        {kind === "galaxy" && (
          <FormField label="Requirements" help="ansible-galaxy install -r: roles and collections, where ansible.cfg says.">
            <Select value={form.requirements ?? p.requirements[0] ?? ""} onChange={(e) => set({ requirements: e.target.value })}>
              {p.requirements.length === 0 && <option value="">No requirements.yml</option>}
              {p.requirements.map((r) => (
                <option key={r} value={r}>
                  {r}
                </option>
              ))}
            </Select>
          </FormField>
        )}
        {kind === "lint" && (
          <FormField label="Playbook" help={p.ansible.lint ? "Empty: the whole project." : "ansible-lint is not installed (pipx install ansible-lint)."}>
            <Select value={form.playbook ?? ""} onChange={(e) => set({ playbook: e.target.value })}>
              <option value="">Whole project</option>
              {p.playbooks.map((pb) => (
                <option key={pb} value={pb}>
                  {pb}
                </option>
              ))}
            </Select>
          </FormField>
        )}
        {(kind === "playbook" || kind === "adhoc") && (
          <>
            <FormField label="Inventory">
              <Select value={form.inventory ?? ""} onChange={(e) => set({ inventory: e.target.value })}>
                <option value="">{p.inventory ? `Project default (${inventoryLabel(p.inventory)})` : "From ansible.cfg"}</option>
                {inventories.map((i) => (
                  <option key={i} value={i}>
                    {inventoryLabel(i)}
                  </option>
                ))}
              </Select>
            </FormField>
            <FormField label="Limit" help="Hosts or groups, e.g. web-1 or web:&prod — start small on production.">
              <Input mono value={form.limit ?? ""} onChange={(e) => set({ limit: e.target.value })} placeholder="all hosts of the play" />
            </FormField>
          </>
        )}
        {kind === "playbook" && (
          <div className="grid grid-cols-2 gap-2">
            <FormField label="Tags">
              <Input mono value={form.tags ?? ""} onChange={(e) => set({ tags: e.target.value })} placeholder="deploy,config" />
            </FormField>
            <FormField label="Skip tags">
              <Input mono value={form.skipTags ?? ""} onChange={(e) => set({ skipTags: e.target.value })} />
            </FormField>
          </div>
        )}
        {(kind === "playbook" || kind === "adhoc") && (
          <>
            <FormField label="Extra variables" help="YAML or JSON, passed with -e.">
              <Textarea mono rows={3} value={form.extraVars ?? ""} onChange={(e) => set({ extraVars: e.target.value })} placeholder={"version: 1.4.2\nserial: 1"} className="text-[12px]" />
            </FormField>
            <div className="flex flex-wrap gap-x-5 gap-y-2">
              <Toggle checked={!!form.check} onChange={(v) => set({ check: v })} label="Check (dry run)" />
              <Toggle checked={!!form.diff} onChange={(v) => set({ diff: v })} label="Diff" />
              {kind === "adhoc" && <Toggle checked={!!form.become} onChange={(v) => set({ become: v })} label="Become" />}
            </div>
            <details className="text-[13px]">
              <summary className="cursor-pointer text-xs text-muted select-none">More options</summary>
              <div className="grid grid-cols-2 gap-2 mt-2">
                <FormField label="Verbosity">
                  <Select value={String(form.verbosity ?? 0)} onChange={(e) => set({ verbosity: Number(e.target.value) })}>
                    {[0, 1, 2, 3, 4].map((v) => (
                      <option key={v} value={v}>
                        {v === 0 ? "normal" : "-" + "v".repeat(v)}
                      </option>
                    ))}
                  </Select>
                </FormField>
                <FormField label="Forks">
                  <Input type="number" min={1} max={200} value={form.forks || ""} onChange={(e) => set({ forks: Number(e.target.value) || 0 })} placeholder="5" />
                </FormField>
                {kind === "playbook" && (
                  <FormField label="Start at task" className="col-span-2">
                    <Input value={form.startAt ?? ""} onChange={(e) => set({ startAt: e.target.value })} placeholder="task name" />
                  </FormField>
                )}
              </div>
            </details>
          </>
        )}
        {locked.length > 0 && (kind === "playbook" || kind === "adhoc") && (
          <div className="rounded-md border border-warn/30 bg-warn/5 px-2.5 py-2 text-xs text-muted">
            Vault {locked.map((id) => <Badge key={id} mono className="mx-0.5">{id}</Badge>)} is locked — you will be asked for the password when the run starts.
          </div>
        )}
        <div className="flex items-center gap-2 pt-1">
          <Button variant="primary" icon={<Play className="size-3.5" />} loading={busy} disabled={!canStart || !p.ansible.installed} onClick={() => void start()} className="flex-1">
            {form.check && (kind === "playbook" || kind === "adhoc") ? "Run check" : "Run"}
          </Button>
        </div>
        {!p.ansible.installed && <div className="text-xs text-err">{p.ansible.error || "ansible is not installed on this computer"}</div>}
        {kind === "playbook" && form.playbook && (
          <div className="flex flex-wrap gap-1.5 pt-1">
            <Button size="sm" variant="ghost" icon={<CheckCheck className="size-3.5" />} loading={checking === "syntax"} onClick={() => void quick("syntax", "Syntax check")}>
              Syntax
            </Button>
            <Button size="sm" variant="ghost" icon={<ListTree className="size-3.5" />} loading={checking === "list-hosts"} onClick={() => void quick("list-hosts", "Hosts")}>
              Hosts
            </Button>
            <Button size="sm" variant="ghost" icon={<ListChecks className="size-3.5" />} loading={checking === "list-tasks"} onClick={() => void quick("list-tasks", "Tasks")}>
              Tasks
            </Button>
            <Button size="sm" variant="ghost" icon={<Tags className="size-3.5" />} loading={checking === "list-tags"} onClick={() => void quick("list-tags", "Tags")}>
              Tags
            </Button>
          </div>
        )}
      </aside>
      <section className="flex-1 min-w-0 flex flex-col min-h-0">
        {runId ? (
          <RunView ws={ws} id={runId} onRerun={rerun} className="flex-1" />
        ) : (
          <EmptyState icon={<Play />} title="Nothing running">
            Choose what to run and start it. The output streams here live, and every run is kept under <b>History</b>.
          </EmptyState>
        )}
      </section>
      <Modal open={!!check} onClose={() => setCheck(null)} title={check?.title ?? ""} width={760} icon={<CheckCheck />}>
        {check && (
          <div className="space-y-2">
            <Badge tone={check.ok ? "ok" : "err"}>{check.ok ? "ok" : "failed"}</Badge>
            <CodeBlock maxHeight={520} className={cx(!check.ok && "text-err")} tone={check.ok ? undefined : "err"}>
              {check.output.trim()}
            </CodeBlock>
          </div>
        )}
      </Modal>
    </div>
  );
}
