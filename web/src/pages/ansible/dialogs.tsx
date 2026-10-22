import { useEffect, useState } from "react";
import { Eye, EyeOff, FilePlus2, KeyRound, Lock, RefreshCw, Search, TextCursorInput } from "lucide-react";
import { api, errMsg } from "../../api/client";
import { Modal, useToast } from "../../components/overlay";
import { Button, CodeBlock, FormField, IconButton, Input, Segmented, Select, Textarea, Toggle } from "../../components/ui";
import { useAnsible } from "./shared";

// Dialogs of the Ansible editor: vault actions, new file, rename, search.

export function VaultIdPicker({ value, onChange }: { value: string; onChange: (v: string) => void }) {
  const { project: p } = useAnsible();
  const ids = Array.from(new Set(["default", ...p.vaultIds]));
  const custom = !ids.includes(value);
  return (
    <FormField label="Vault id" help="Projects with one password use “default”. Other ids are labels for separate passwords (e.g. prod, staging).">
      <div className="flex gap-2">
        <Select value={custom ? "__custom" : value} onChange={(e) => onChange(e.target.value === "__custom" ? "" : e.target.value)} className="flex-1">
          {ids.map((id) => (
            <option key={id} value={id}>
              {id}
              {p.unlocked.includes(id) ? " · unlocked" : ""}
            </option>
          ))}
          <option value="__custom">Another id…</option>
        </Select>
        {custom && <Input mono autoFocus value={value} onChange={(e) => onChange(e.target.value.replace(/[;\s]/g, ""))} placeholder="prod" className="w-36" />}
      </div>
    </FormField>
  );
}

export function EncryptModal({ path, onClose, onDone }: { path: string; onClose: () => void; onDone: () => void }) {
  const { ws, project: p, withPassword } = useAnsible();
  const toast = useToast();
  const [vaultId, setVaultId] = useState(p.vaultIds[0] ?? "default");
  const [busy, setBusy] = useState(false);
  async function go() {
    setBusy(true);
    try {
      const r = await withPassword((pw) => api.ansibleVault(ws, p.name, "encrypt", { path, vaultId: vaultId || "default", password: pw?.password }));
      if (r) {
        toast.success(`${path} encrypted`);
        onDone();
      }
    } catch (e) {
      toast.error("Could not encrypt", errMsg(e));
    } finally {
      setBusy(false);
    }
  }
  return (
    <Modal
      open
      onClose={onClose}
      title={`Encrypt ${path}`}
      icon={<Lock />}
      width={480}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" loading={busy} onClick={() => void go()}>
            Encrypt
          </Button>
        </>
      }
    >
      <div className="space-y-3">
        <p className="text-[13px] text-muted">The whole file is encrypted in place (ansible-vault encrypt). Ansible decrypts it when it runs.</p>
        <VaultIdPicker value={vaultId} onChange={setVaultId} />
      </div>
    </Modal>
  );
}

export function RekeyModal({ path, vaultId, onClose, onDone }: { path: string; vaultId: string; onClose: () => void; onDone: () => void }) {
  const { ws, project: p, withPassword } = useAnsible();
  const toast = useToast();
  const [pw, setPw] = useState("");
  const [pw2, setPw2] = useState("");
  const [newId, setNewId] = useState(vaultId);
  const [busy, setBusy] = useState(false);
  const ok = pw.length > 0 && pw === pw2;
  async function go() {
    setBusy(true);
    try {
      const r = await withPassword((cur) => api.ansibleVault(ws, p.name, "rekey", { path, vaultId, password: cur?.password, newPassword: pw, newVaultId: newId }));
      if (r) {
        toast.success("Password changed", `${path} is now encrypted with the new password. Other files with vault id ${vaultId} keep the old one.`);
        onDone();
      }
    } catch (e) {
      toast.error("Could not change the password", errMsg(e));
    } finally {
      setBusy(false);
    }
  }
  return (
    <Modal
      open
      onClose={onClose}
      title="Change the vault password"
      icon={<RefreshCw />}
      width={480}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" loading={busy} disabled={!ok} onClick={() => void go()}>
            Re-encrypt
          </Button>
        </>
      }
    >
      <div className="space-y-3">
        <p className="text-[13px] text-muted">
          Re-encrypts <span className="font-mono">{path}</span> (ansible-vault rekey). You are asked for the current password if it is not unlocked.
        </p>
        <FormField label="New password">
          <Input type="password" autoComplete="new-password" autoFocus value={pw} onChange={(e) => setPw(e.target.value)} />
        </FormField>
        <FormField label="Repeat">
          <Input type="password" autoComplete="new-password" value={pw2} onChange={(e) => setPw2(e.target.value)} />
        </FormField>
        {pw2 && pw !== pw2 && <div className="text-xs text-err">The passwords differ.</div>}
        <VaultIdPicker value={newId} onChange={setNewId} />
      </div>
    </Modal>
  );
}

function genPassword(n = 24): string {
  const chars = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789";
  const buf = new Uint32Array(n);
  crypto.getRandomValues(buf);
  return Array.from(buf, (x) => chars[x % chars.length]).join("");
}

export function SetVarModal({ path, encrypted, initialKey = "", onClose, onDone }: { path: string; encrypted: boolean; initialKey?: string; onClose: () => void; onDone: () => void }) {
  const { ws, project: p, withPassword } = useAnsible();
  const toast = useToast();
  const [key, setKey] = useState(initialKey);
  const [value, setValue] = useState("");
  const [show, setShow] = useState(false);
  const [vaultId, setVaultId] = useState(p.vaultIds[0] ?? "default");
  const [busy, setBusy] = useState(false);
  const valid = /^[A-Za-z_][A-Za-z0-9_]*$/.test(key) && value !== "";
  async function go() {
    setBusy(true);
    try {
      const r = await withPassword((pw) => api.ansibleVault(ws, p.name, "set", { path, key, value, vaultId: vaultId || "default", password: pw?.password }));
      if (r) {
        toast.success(`${key} set`, encrypted ? "Inside the encrypted file." : "As an encrypted !vault value.");
        onDone();
      }
    } catch (e) {
      toast.error("Could not set it", errMsg(e));
    } finally {
      setBusy(false);
    }
  }
  return (
    <Modal
      open
      onClose={onClose}
      title={encrypted ? "Set a variable" : "Add an encrypted variable"}
      icon={<KeyRound />}
      width={500}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" loading={busy} disabled={!valid} onClick={() => void go()}>
            Save
          </Button>
        </>
      }
    >
      <div className="space-y-3">
        <p className="text-[13px] text-muted">
          {encrypted ? (
            <>
              Sets a top-level variable inside <span className="font-mono">{path}</span>, which stays encrypted.
            </>
          ) : (
            <>
              Writes <span className="font-mono">name: !vault |…</span> into <span className="font-mono">{path}</span>; the rest of the file stays readable.
            </>
          )}{" "}
          An existing variable of that name is replaced.
        </p>
        <FormField label="Name">
          <Input mono autoFocus value={key} onChange={(e) => setKey(e.target.value.trim())} placeholder="db_password" />
        </FormField>
        <FormField label="Value">
          <div className="flex gap-2">
            <Input mono type={show ? "text" : "password"} autoComplete="off" value={value} onChange={(e) => setValue(e.target.value)} className="flex-1" />
            <IconButton title={show ? "Hide" : "Show"} onClick={() => setShow(!show)}>
              {show ? <EyeOff className="size-4" /> : <Eye className="size-4" />}
            </IconButton>
            <Button size="md" onClick={() => { setValue(genPassword()); setShow(true); }}>
              Generate
            </Button>
          </div>
        </FormField>
        {!encrypted && <VaultIdPicker value={vaultId} onChange={setVaultId} />}
      </div>
    </Modal>
  );
}

export function EncryptStringModal({ onClose }: { onClose: () => void }) {
  const { ws, project: p, withPassword } = useAnsible();
  const toast = useToast();
  const [name, setName] = useState("");
  const [value, setValue] = useState("");
  const [vaultId, setVaultId] = useState(p.vaultIds[0] ?? "default");
  const [out, setOut] = useState("");
  const [busy, setBusy] = useState(false);
  async function go() {
    setBusy(true);
    try {
      const r = await withPassword((pw) => api.ansibleVault<{ text: string }>(ws, p.name, "encrypt-string", { name, value, vaultId: vaultId || "default", password: pw?.password }));
      if (r) setOut(r.text);
    } catch (e) {
      toast.error("Could not encrypt", errMsg(e));
    } finally {
      setBusy(false);
    }
  }
  return (
    <Modal
      open
      onClose={onClose}
      title="Encrypt a value"
      icon={<TextCursorInput />}
      width={620}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Close
          </Button>
          <Button variant="primary" loading={busy} disabled={!value} onClick={() => void go()}>
            Encrypt
          </Button>
        </>
      }
    >
      <div className="space-y-3">
        <p className="text-[13px] text-muted">Like ansible-vault encrypt_string: paste the result into any YAML variables file.</p>
        <div className="grid grid-cols-2 gap-2">
          <FormField label="Variable name (optional)">
            <Input mono value={name} onChange={(e) => setName(e.target.value.trim())} placeholder="api_token" />
          </FormField>
          <VaultIdPicker value={vaultId} onChange={setVaultId} />
        </div>
        <FormField label="Value">
          <Textarea mono rows={3} value={value} onChange={(e) => setValue(e.target.value)} autoComplete="off" />
        </FormField>
        {out && <CodeBlock maxHeight={260}>{out}</CodeBlock>}
      </div>
    </Modal>
  );
}

export function NewFileModal({ open, dir, vaultIds, onClose, onCreated }: { open: boolean; dir: string; vaultIds: string[]; onClose: () => void; onCreated: (p: string) => Promise<void> }) {
  const { ws, project: p, withPassword } = useAnsible();
  const toast = useToast();
  const [path, setPath] = useState("");
  const [encrypted, setEncrypted] = useState(false);
  const [vaultId, setVaultId] = useState(vaultIds[0] ?? "default");
  const [template, setTemplate] = useState<"empty" | "playbook" | "vars">("empty");
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    if (open) {
      setPath(dir);
      setEncrypted(false);
    }
  }, [open, dir]);
  const clean = path.trim().replace(/^\/+/, "");
  const valid = clean !== "" && !clean.endsWith("/") && !clean.split("/").some((s) => s === ".." || s.toLowerCase() === ".git" || s === "");
  const content =
    template === "playbook"
      ? "---\n- name: Describe what this play does\n  hosts: all\n  become: true\n  tasks:\n    - name: Example task\n      ansible.builtin.debug:\n        msg: \"hello from {{ inventory_hostname }}\"\n"
      : template === "vars"
        ? "---\n# variables\n"
        : "";
  async function create() {
    setBusy(true);
    try {
      if (encrypted) {
        const r = await withPassword((pw) => api.ansibleVault(ws, p.name, "save", { path: clean, text: content || "---\n", vaultId: vaultId || "default", password: pw?.password }));
        if (!r) return;
      } else {
        await api.ansibleWrite(ws, p.name, clean, content, true);
      }
      await onCreated(clean);
    } catch (e) {
      toast.error("Cannot create", errMsg(e));
    } finally {
      setBusy(false);
    }
  }
  return (
    <Modal
      open={open}
      onClose={onClose}
      title="New file"
      icon={<FilePlus2 />}
      width={500}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" loading={busy} disabled={!valid} onClick={() => void create()}>
            Create
          </Button>
        </>
      }
    >
      <div className="space-y-3">
        <FormField label="Path" help="Relative to the project folder; folders are created as needed.">
          <Input mono autoFocus value={path} onChange={(e) => setPath(e.target.value)} placeholder="group_vars/prod/vault.yml" />
        </FormField>
        <FormField label="Start from">
          <Segmented
            value={template}
            onChange={setTemplate}
            options={[
              { id: "empty", label: "Empty" },
              { id: "playbook", label: "Playbook" },
              { id: "vars", label: "Variables" },
            ]}
          />
        </FormField>
        <Toggle checked={encrypted} onChange={setEncrypted} label="Encrypt with Ansible Vault" />
        {encrypted && <VaultIdPicker value={vaultId} onChange={setVaultId} />}
      </div>
    </Modal>
  );
}

export function RenameModal({ path, onClose, onDone }: { path: string; onClose: () => void; onDone: (to: string) => void }) {
  const { ws, project: p } = useAnsible();
  const toast = useToast();
  const [to, setTo] = useState(path);
  const [busy, setBusy] = useState(false);
  async function go() {
    setBusy(true);
    try {
      await api.ansibleRename(ws, p.name, path, to.trim());
      onDone(to.trim());
    } catch (e) {
      toast.error("Cannot rename", errMsg(e));
    } finally {
      setBusy(false);
    }
  }
  return (
    <Modal
      open
      onClose={onClose}
      title="Rename"
      icon={<TextCursorInput />}
      width={480}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" loading={busy} disabled={!to.trim() || to.trim() === path} onClick={() => void go()}>
            Rename
          </Button>
        </>
      }
    >
      <FormField label="New path">
        <Input mono autoFocus value={to} onChange={(e) => setTo(e.target.value)} onKeyDown={(e) => e.key === "Enter" && void go()} />
      </FormField>
    </Modal>
  );
}

export function SearchModal({ onClose, onOpen }: { onClose: () => void; onOpen: (path: string) => void }) {
  const { ws, project: p } = useAnsible();
  const [q, setQ] = useState("");
  const [regex, setRegex] = useState(false);
  const [hits, setHits] = useState<{ path: string; line: number; text: string }[] | null>(null);
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);
  async function go() {
    if (!q) return;
    setBusy(true);
    setErr("");
    try {
      setHits(await api.ansibleSearch(ws, p.name, q, regex));
    } catch (e) {
      setErr(errMsg(e));
    } finally {
      setBusy(false);
    }
  }
  return (
    <Modal open onClose={onClose} title="Search in files" icon={<Search />} width={760}>
      <div className="space-y-3">
        <div className="flex gap-2 items-center">
          <Input mono autoFocus value={q} onChange={(e) => setQ(e.target.value)} onKeyDown={(e) => e.key === "Enter" && void go()} placeholder="nginx_port" className="flex-1" />
          <Toggle checked={regex} onChange={setRegex} label="Regex" />
          <Button variant="primary" loading={busy} onClick={() => void go()}>
            Search
          </Button>
        </div>
        {err && <div className="text-xs text-err">{err}</div>}
        {hits && (
          <div className="max-h-[60vh] overflow-y-auto rounded-md border border-line divide-y divide-line">
            {hits.length === 0 && <div className="p-3 text-xs text-muted">No matches (vault files are not searched).</div>}
            {hits.map((h, i) => (
              <button key={i} onClick={() => onOpen(h.path)} className="w-full text-left px-3 py-1.5 hover:bg-panel-2 font-mono text-[11.5px] flex gap-2">
                <span className="text-accent shrink-0">
                  {h.path}:{h.line}
                </span>
                <span className="truncate text-muted">{h.text}</span>
              </button>
            ))}
          </div>
        )}
      </div>
    </Modal>
  );
}
