import { useCallback, useEffect, useMemo, useState } from "react";
import { AskAgentButton } from "../../components/AskAgent";
import { useSearchParams } from "react-router-dom";
import { FolderGit2, FolderOpen, FolderPlus, KeyRound, Lock, LockOpen, Pencil, Plus, RefreshCw, Workflow } from "lucide-react";
import { api, errMsg } from "../../api/client";
import type { AnsibleProject, Resource } from "../../api/types";
import { useWs } from "../../context/app";
import { useTabTitle } from "../../context/tabs";
import { Modal, useConfirm, useToast } from "../../components/overlay";
import { Badge, Button, EmptyState, ErrorBox, FormField, IconButton, Input, Loading, PageHeader, Segmented, Select, Tabs, cx } from "../../components/ui";
import { SecretRefPicker } from "../../components/VaultItemForm";
import { useAsync, useLocalStorage } from "../../hooks";
import { slugify } from "../../lib/format";
import { EditorTab } from "./Workbench";
import { HistoryTab } from "./History";
import { InventoryTab } from "./Inventory";
import { RunTab } from "./Run";
import { AnsibleProvider, usePasswordPrompt } from "./shared";
import type { PasswordAnswer } from "./shared";

type Tab = "files" | "run" | "runs" | "inventory";

// Ansible: the workspace's Ansible projects (a folder, or a folder of one of
// its Git repositories) — files with a vault-aware editor, runs with live
// logs and their history, and inventories. Vault passwords are asked for
// when needed and kept in memory, or saved in the vault.
export function AnsiblePage() {
  const { name: ws } = useWs();
  const list = useAsync(() => api.ansibleProjects(ws), [ws]);
  const [params, setParams] = useSearchParams();
  const [saved, setSaved] = useLocalStorage<string>(`opsdeck.ansible.${ws}`, "");
  const projects = list.data ?? [];
  const name = params.get("project") || (projects.some((p) => p.name === saved) ? saved : projects[0]?.name) || "";
  const tab = (params.get("tab") as Tab) || "files";
  const [adding, setAdding] = useState(false);
  const [editing, setEditing] = useState(false);

  const set = useCallback(
    (patch: Record<string, string | null>) =>
      setParams(
        (p) => {
          const n = new URLSearchParams(p);
          for (const [k, v] of Object.entries(patch)) {
            if (v === null || v === "") n.delete(k);
            else n.set(k, v);
          }
          return n;
        },
        { replace: true },
      ),
    [setParams],
  );
  useEffect(() => {
    if (name) setSaved(name);
  }, [name, setSaved]);
  useTabTitle(name ? `Ansible · ${name}` : "Ansible");

  const project = projects.find((p) => p.name === name);

  if (!list.data && list.loading) return <Loading />;
  if (list.error && !list.data) return <ErrorBox error={list.error} onRetry={() => void list.reload()} />;

  return (
    <div className="flex flex-col h-full min-h-0">
      <PageHeader
        icon={<Workflow />}
        title="Ansible"
        subtitle={project ? <ProjectLine p={project} /> : "Playbooks, inventories and vault files — from a folder or a Git repository."}
        actions={
          <>
            <AskAgentButton prompt={"Explain what the playbooks of this Ansible project do and which hosts they target."} />
            {projects.length > 0 && (
              <>
                <IconButton title="Refresh" onClick={() => void list.reload()}>
                  <RefreshCw className={cx("size-4", list.loading && "animate-spin")} />
                </IconButton>
                <Select value={name} onChange={(e) => set({ project: e.target.value, path: null, run: null, playbook: null })} className="w-56">
                  {projects.map((p) => (
                    <option key={p.name} value={p.name}>
                      {p.name}
                      {p.environment ? ` · ${p.environment}` : ""}
                    </option>
                  ))}
                </Select>
                {project && (
                  <IconButton title="Project settings" onClick={() => setEditing(true)}>
                    <Pencil className="size-4" />
                  </IconButton>
                )}
              </>
            )}
            <Button icon={<Plus className="size-3.5" />} onClick={() => setAdding(true)}>
              Add project
            </Button>
          </>
        }
      />
      {!project ? (
        <EmptyState
          icon={<Workflow />}
          title="No Ansible projects yet"
          action={
            <Button variant="primary" icon={<Plus className="size-3.5" />} onClick={() => setAdding(true)}>
              Add project
            </Button>
          }
        >
          Start a new project in the workspace, use a folder you already have, or a folder of one of the workspace's Git repositories. You and the agent can then edit
          it, run playbooks with live logs, and work with Ansible Vault.
        </EmptyState>
      ) : (
        <ProjectView key={project.name} project={project} tab={tab} set={set} reload={list.reload} />
      )}
      {adding && (
        <ProjectModal
          onClose={() => setAdding(false)}
          onSaved={async (n) => {
            setAdding(false);
            await list.reload();
            set({ project: n, tab: "files", path: null, run: null });
          }}
        />
      )}
      {editing && project && (
        <ProjectModal
          edit={project}
          onClose={() => setEditing(false)}
          onSaved={async (n) => {
            setEditing(false);
            await list.reload();
            set({ project: n });
          }}
        />
      )}
    </div>
  );
}

function ProjectLine({ p }: { p: AnsibleProject }) {
  return (
    <span className="inline-flex items-center gap-1.5 flex-wrap">
      {p.source === "repo" ? <FolderGit2 className="size-3.5" /> : <FolderOpen className="size-3.5" />}
      <span className="font-mono text-[12px]">{p.source === "repo" ? `${p.repo}/${p.path || ""}` : p.root}</span>
      {p.ansible.installed ? <Badge>{p.ansible.version}</Badge> : <Badge tone="err">ansible not installed</Badge>}
      {p.via && <Badge mono>via {p.via}</Badge>}
    </span>
  );
}

function ProjectView({ project: p, tab, set, reload }: { project: AnsibleProject; tab: Tab; set: (patch: Record<string, string | null>) => void; reload: () => Promise<void> }) {
  const { name: ws } = useWs();
  const toast = useToast();
  const unlock = useCallback(
    async (vaultId: string, a: PasswordAnswer) => {
      const r = await api.ansibleUnlock(ws, p.name, { vaultId, password: a.password, save: a.save, agentUse: a.agentUse });
      if (r.saved) toast.success("Password saved", `${r.saved} — used for this project from now on.`);
      void reload();
    },
    [ws, p.name, reload, toast],
  );
  const { withPassword, dialog } = usePasswordPrompt(p.name, unlock);
  const ctx = useMemo(
    () => ({
      ws,
      project: p,
      reload,
      withPassword,
      goto: (t: string, extra: Record<string, string> = {}) => set({ tab: t, ...extra }),
    }),
    [ws, p, reload, withPassword, set],
  );

  const tabs: { id: Tab; label: React.ReactNode; count?: number }[] = [
    { id: "files", label: "Editor", count: p.files },
    { id: "run", label: "Run" },
    { id: "runs", label: "History" },
    { id: "inventory", label: "Inventory" },
  ];

  return (
    <AnsibleProvider value={ctx}>
      <div className="flex items-center gap-3 px-3 border-b border-line">
        <Tabs className="border-b-0 flex-1" value={tab} onChange={(t) => set({ tab: t })} tabs={tabs} />
        <VaultStatus p={p} reload={reload} />
      </div>
      <div className="flex-1 min-h-0 p-5">
        {p.error ? (
          <ErrorBox error={p.error} onRetry={() => void reload()} />
        ) : tab === "files" ? (
          <EditorTab />
        ) : tab === "run" ? (
          <RunTab />
        ) : tab === "runs" ? (
          <HistoryTab />
        ) : (
          <InventoryTab />
        )}
      </div>
      {dialog}
    </AnsibleProvider>
  );
}

// VaultStatus shows each vault id of the project: unlocked (in memory or
// saved) or locked, with unlock and lock buttons.
function VaultStatus({ p, reload }: { p: AnsibleProject; reload: () => Promise<void> }) {
  const { name: ws } = useWs();
  const toast = useToast();
  const [unlocking, setUnlocking] = useState<string | null>(null);
  const ids = Array.from(new Set([...p.vaultIds, ...p.remembered, ...p.saved]));
  if (ids.length === 0) return null;
  async function lock(id: string) {
    try {
      await api.ansibleLock(ws, p.name, id);
      toast.info(`Vault ${id} locked`, p.saved.includes(id) ? "It is still saved in the vault and used from there." : "Its password was forgotten.");
      void reload();
    } catch (e) {
      toast.error("Could not lock", errMsg(e));
    }
  }
  return (
    <div className="flex items-center gap-1.5 py-1.5">
      <KeyRound className="size-3.5 text-faint" />
      {ids.map((id) => {
        const open = p.unlocked.includes(id);
        const title = p.saved.includes(id) ? "Password saved in the vault" : p.remembered.includes(id) ? "Password in memory until opsdeck locks — click to forget" : "Locked — click to unlock";
        return (
          <button
            key={id}
            title={title}
            onClick={() => (p.remembered.includes(id) ? void lock(id) : open ? undefined : setUnlocking(id))}
            className={cx("inline-flex items-center gap-1 rounded border px-1.5 h-5 text-[11px] font-mono", open ? "border-ok/30 text-ok bg-ok/10" : "border-warn/30 text-warn bg-warn/10")}
          >
            {open ? <LockOpen className="size-3" /> : <Lock className="size-3" />}
            {id}
          </button>
        );
      })}
      {unlocking && <UnlockModal p={p} vaultId={unlocking} onClose={() => setUnlocking(null)} onDone={() => void reload()} />}
    </div>
  );
}

function UnlockModal({ p, vaultId, onClose, onDone }: { p: AnsibleProject; vaultId: string; onClose: () => void; onDone: () => void }) {
  const { name: ws } = useWs();
  const toast = useToast();
  const [password, setPassword] = useState("");
  const [save, setSave] = useState(false);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  async function go() {
    setBusy(true);
    setErr("");
    try {
      const r = await api.ansibleUnlock(ws, p.name, { vaultId, password, save, agentUse: true });
      toast.success(`Vault ${vaultId} unlocked`, r.saved ? `Saved as ${r.saved}.` : r.checked ? "Kept in memory until opsdeck locks." : "Nothing encrypted with this id yet to check it against.");
      onDone();
      onClose();
    } catch (e) {
      setErr(errMsg(e));
    } finally {
      setBusy(false);
    }
  }
  return (
    <Modal
      open
      onClose={onClose}
      title={`Unlock vault ${vaultId}`}
      icon={<KeyRound />}
      width={440}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" loading={busy} disabled={!password} onClick={() => void go()}>
            Unlock
          </Button>
        </>
      }
    >
      <div className="space-y-3">
        <p className="text-[13px] text-muted">Kept in memory until opsdeck locks; runs, views and the agent use it without asking again.</p>
        <Input type="password" autoFocus autoComplete="off" value={password} onChange={(e) => setPassword(e.target.value)} onKeyDown={(e) => e.key === "Enter" && void go()} placeholder="Vault password" />
        {err && <div className="text-xs text-err">{err}</div>}
        <label className="flex items-center gap-2 text-[13px]">
          <input type="checkbox" checked={save} onChange={(e) => setSave(e.target.checked)} />
          Also save it in the vault of this workspace
        </label>
      </div>
    </Modal>
  );
}

// ---------------------------------------------------------------------------
// Adding and editing a project

type Source = "new" | "folder" | "repo";

function ProjectModal({ edit, onClose, onSaved }: { edit?: AnsibleProject; onClose: () => void; onSaved: (name: string) => Promise<void> }) {
  const { name: ws, workspace, byKind, resources, reloadResources } = useWs();
  const toast = useToast();
  const confirm = useConfirm();
  const repos = byKind("GitRemote");
  const res = edit ? resources.find((r) => r.kind === "AnsibleProject" && r.name === edit.name) : undefined;
  const [source, setSource] = useState<Source>(edit ? (edit.source === "repo" ? "repo" : "folder") : "new");
  const [name, setName] = useState(edit?.name ?? "");
  const [repo, setRepo] = useState(edit?.repo ?? repos[0]?.name ?? "");
  const [path, setPath] = useState(edit?.path ?? "");
  const [inventory, setInventory] = useState(edit?.inventory ?? "");
  const [environment, setEnvironment] = useState(edit?.environment ?? "");
  const [description, setDescription] = useState(edit?.description ?? "");
  const [via, setVia] = useState(edit?.via ?? "");
  const [sshKey, setSshKey] = useState(String(res?.spec.sshKey ?? ""));
  const [bin, setBin] = useState(String(res?.spec.bin ?? ""));
  const [busy, setBusy] = useState(false);
  const vias = resources.filter((r) => r.kind === "Host" || ["WireGuard", "Tailscale", "NetBird", "OpenVPN", "OpenConnect", "OpenFortiVPN", "SocksProxy"].includes(r.kind));
  const slug = slugify(name);
  const valid = !!slug && (source !== "repo" || !!repo) && (source !== "folder" || !!path.trim() || !!edit);

  async function save() {
    setBusy(true);
    try {
      if (edit && res) {
        const next: Resource = {
          ...res,
          environment: environment || undefined,
          description: description || undefined,
          via: via || undefined,
          spec: { ...res.spec, source: source === "repo" ? "repo" : "folder", repo: source === "repo" ? repo : "", path, inventory, sshKey, bin },
        };
        await api.updateResource(ws, "AnsibleProject", edit.name, next);
      } else {
        await api.ansibleCreate(ws, {
          name: slug,
          source: source === "repo" ? "repo" : "folder",
          repo: source === "repo" ? repo : undefined,
          path: path.trim() || undefined,
          inventory: inventory || undefined,
          environment: environment || undefined,
          description: description || undefined,
          via: via || undefined,
          scaffold: source === "new",
        });
      }
      await reloadResources();
      await onSaved(edit ? edit.name : slug);
    } catch (e) {
      toast.error("Could not save", errMsg(e));
    } finally {
      setBusy(false);
    }
  }

  async function remove() {
    if (!edit) return;
    const ok = await confirm({
      title: `Remove ${edit.name}?`,
      message: "The project is removed from the workspace. Its files stay where they are.",
      confirmLabel: "Remove",
      danger: true,
    });
    if (!ok) return;
    try {
      await api.deleteResource(ws, "AnsibleProject", edit.name);
      await reloadResources();
      await onSaved("");
    } catch (e) {
      toast.error("Could not remove", errMsg(e));
    }
  }

  return (
    <Modal
      open
      onClose={onClose}
      title={edit ? `Project ${edit.name}` : "Add an Ansible project"}
      icon={edit ? <Pencil /> : <FolderPlus />}
      width={560}
      footer={
        <>
          {edit && (
            <Button variant="ghost" className="mr-auto text-err" onClick={() => void remove()}>
              Remove
            </Button>
          )}
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" loading={busy} disabled={!valid} onClick={() => void save()}>
            {edit ? "Save" : source === "new" ? "Create" : "Add"}
          </Button>
        </>
      }
    >
      <div className="space-y-3">
        {!edit && (
          <Segmented<Source>
            value={source}
            onChange={setSource}
            options={[
              { id: "new", label: <><FolderPlus className="size-3.5" /> New folder</> },
              { id: "folder", label: <><FolderOpen className="size-3.5" /> Existing folder</> },
              { id: "repo", label: <><FolderGit2 className="size-3.5" /> Git repository</>, title: repos.length ? undefined : "Add a repository on the Repositories page first" },
            ]}
          />
        )}
        <FormField label="Name" help={!edit && slug && slug !== name ? <>Saved as <span className="font-mono">{slug}</span></> : undefined}>
          <Input autoFocus={!edit} disabled={!!edit} value={name} onChange={(e) => setName(e.target.value)} placeholder="infra" />
        </FormField>
        {source === "repo" ? (
          repos.length === 0 ? (
            <div className="rounded-md border border-warn/30 bg-warn/5 p-2.5 text-xs text-muted">The workspace has no Git repositories yet: add one on the Repositories page, then come back.</div>
          ) : (
            <div className="grid grid-cols-2 gap-2">
              <FormField label="Repository">
                <Select value={repo} onChange={(e) => setRepo(e.target.value)}>
                  {repos.map((r) => (
                    <option key={r.name} value={r.name}>
                      {r.name}
                    </option>
                  ))}
                </Select>
              </FormField>
              <FormField label="Folder in the repository" help="Empty: the root.">
                <Input mono value={path} onChange={(e) => setPath(e.target.value)} placeholder="ansible" />
              </FormField>
            </div>
          )
        ) : (
          <FormField
            label="Folder"
            help={
              source === "new"
                ? <>Inside the workspace (shared with it); empty: <span className="font-mono">ansible/{slug || "<name>"}</span>. A starter layout is created: ansible.cfg, inventory, site.yml.</>
                : "A folder of the workspace (relative path) or anywhere on this computer (absolute or ~/…)."
            }
          >
            <Input mono value={path} onChange={(e) => setPath(e.target.value)} placeholder={source === "new" ? `ansible/${slug || "infra"}` : "~/work/ansible"} />
          </FormField>
        )}
        <div className="grid grid-cols-2 gap-2">
          <FormField label="Default inventory" help="Empty: ansible.cfg's.">
            <Input mono value={inventory} onChange={(e) => setInventory(e.target.value)} placeholder="inventories/prod" list="ansible-inv" />
            <datalist id="ansible-inv">
              <option value="workspace">the workspace's hosts</option>
              {edit?.inventories.map((i) => <option key={i} value={i} />)}
            </datalist>
          </FormField>
          <FormField label="Environment" help="Protected environments make the agent ask before runs.">
            <Select value={environment} onChange={(e) => setEnvironment(e.target.value)}>
              <option value="">—</option>
              {(workspace?.environments ?? []).map((e) => (
                <option key={e.name} value={e.name}>
                  {e.name}
                  {e.protected ? " (protected)" : ""}
                </option>
              ))}
            </Select>
          </FormField>
        </div>
        <FormField label="Description">
          <Input value={description} onChange={(e) => setDescription(e.target.value)} placeholder="What it manages" />
        </FormField>
        <details className="text-[13px]">
          <summary className="cursor-pointer text-xs text-muted select-none">Advanced</summary>
          <div className="space-y-3 mt-2">
            <FormField label="Reach the hosts through" help="For the project's own inventory: a VPN or jump host of the workspace. Workspace hosts always use their own route.">
              <Select value={via} onChange={(e) => setVia(e.target.value)}>
                <option value="">Directly</option>
                {vias.map((r) => (
                  <option key={r.kind + r.name} value={(r.kind === "Host" ? "host/" : "network/") + r.name}>
                    {(r.kind === "Host" ? "host/" : "network/") + r.name}
                  </option>
                ))}
              </Select>
            </FormField>
            {edit && (
              <>
                <FormField label="SSH key" help="Served to ansible by an in-memory ssh-agent. Workspace hosts use their own credentials.">
                  <SecretRefPicker value={sshKey} onChange={setSshKey} ws={ws} itemTypes={["ssh-key"]} />
                </FormField>
                <FormField label="Ansible folder" help="Folder holding ansible-playbook, e.g. a virtualenv. Empty: from PATH.">
                  <Input mono value={bin} onChange={(e) => setBin(e.target.value)} placeholder="~/.venvs/ansible/bin" />
                </FormField>
              </>
            )}
          </div>
        </details>
      </div>
    </Modal>
  );
}
