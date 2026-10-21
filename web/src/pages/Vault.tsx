import { useEffect, useMemo, useState } from "react";
import { Eye, EyeOff, FileUp, KeyRound, Pencil, Plus, ShieldCheck, Sparkles, Trash2 } from "lucide-react";
import { api, errMsg } from "../api/client";
import type { Field, Item } from "../api/types";
import { useApp, useWs } from "../context/app";
import {
  Badge,
  Button,
  ChipInput,
  CodeBlock,
  CopyButton,
  EmptyState,
  ErrorBox,
  FormField,
  IconButton,
  Input,
  Loading,
  SearchInput,
  Segmented,
  Select,
  Textarea,
  Toggle,
  cx,
} from "../components/ui";
import { Modal, useConfirm, useToast } from "../components/overlay";
import { ItemTypeIcon } from "../components/icons";
import { VaultItemModal } from "../components/VaultItemForm";
import { useAsync, useInterval } from "../hooks";
import { fmtTime } from "../lib/format";

export function VaultPage() {
  const { name: ws } = useWs();
  const { itemTypes } = useApp();
  const [scope, setScope] = useState<string>(ws);
  const items = useAsync(() => api.items(scope), [scope]);
  const [q, setQ] = useState("");
  const [type, setType] = useState("");
  const [selId, setSelId] = useState<string | null>(null);
  const [editItem, setEditItem] = useState<Item | null>(null);
  const [createOpen, setCreateOpen] = useState(false);
  const [genOpen, setGenOpen] = useState(false);
  const [importOpen, setImportOpen] = useState(false);

  useEffect(() => setScope(ws), [ws]);

  const filtered = useMemo(() => {
    const t = q.toLowerCase();
    return (items.data ?? [])
      .filter((i) => !type || i.type === type)
      .filter((i) => !t || i.name.toLowerCase().includes(t) || i.tags.some((x) => x.toLowerCase().includes(t)) || i.type.includes(t))
      .sort((a, b) => a.name.localeCompare(b.name));
  }, [items.data, q, type]);
  const sel = (items.data ?? []).find((i) => i.id === selId) ?? null;
  const scopes = [
    { id: ws, label: `Workspace: ${ws}` },
    { id: "global", label: "Global" },
  ];

  const onSaved = (it: Item) => {
    if (it.scope !== scope) setScope(it.scope);
    void items.reload().then(() => setSelId(it.id));
  };

  return (
    <div className="flex h-full min-h-0">
      <aside className="w-[340px] shrink-0 border-r border-line bg-panel flex flex-col">
        <div className="p-3 space-y-2 border-b border-line">
          <div className="flex items-center justify-between">
            <span className="font-semibold flex items-center gap-2">
              <ShieldCheck className="size-4 text-accent" /> Vault
            </span>
            <Button size="xs" variant="primary" icon={<Plus className="size-3" />} onClick={() => setCreateOpen(true)}>
              New
            </Button>
          </div>
          <Segmented
            value={scope === "global" ? "global" : "ws"}
            onChange={(v) => {
              setScope(v === "global" ? "global" : ws);
              setSelId(null);
            }}
            options={[
              { id: "ws", label: `This workspace` },
              { id: "global", label: "Global" },
            ]}
          />
          <div className="flex gap-2">
            <SearchInput value={q} onChange={setQ} placeholder="Search items…" className="flex-1" />
            <Select value={type} onChange={(e) => setType(e.target.value)} className="w-28">
              <option value="">All types</option>
              {itemTypes.map((t) => (
                <option key={t.type} value={t.type}>
                  {t.label}
                </option>
              ))}
            </Select>
          </div>
          <div className="flex gap-1.5">
            <Button size="xs" icon={<Sparkles className="size-3" />} onClick={() => setGenOpen(true)}>
              Generate SSH key
            </Button>
            <Button size="xs" icon={<FileUp className="size-3" />} onClick={() => setImportOpen(true)}>
              Import SSH key
            </Button>
          </div>
        </div>
        <div className="flex-1 overflow-y-auto">
          {items.error && <ErrorBox error={items.error} onRetry={() => void items.reload()} />}
          {!items.data && items.loading && <Loading />}
          {items.data && filtered.length === 0 && (
            <EmptyState icon={<KeyRound />} title={items.data.length ? "No matches" : "No items yet"}>
              {!items.data.length && (scope === "global" ? "Global items are usable from every workspace (e.g. your personal SSH key, API keys)." : "Items here are only usable inside this workspace.")}
            </EmptyState>
          )}
          {filtered.map((i) => (
            <button
              key={i.id}
              onClick={() => setSelId(i.id)}
              className={cx(
                "w-full flex items-center gap-2.5 px-3 h-12 text-left border-b border-line/60",
                selId === i.id ? "bg-accent/10" : "hover:bg-panel-2",
              )}
            >
              <span className={cx("size-7 rounded-md grid place-items-center shrink-0", selId === i.id ? "bg-accent/20 text-accent" : "bg-panel-3 text-muted")}>
                <ItemTypeIcon type={i.type} className="size-3.5" />
              </span>
              <span className="min-w-0 flex-1">
                <span className="block truncate text-[13px] font-medium font-mono">{i.name}</span>
                <span className="block truncate text-[11px] text-faint">
                  {itemTypes.find((t) => t.type === i.type)?.label ?? i.type}
                  {i.tags.length > 0 && ` · ${i.tags.join(", ")}`}
                </span>
              </span>
              {!i.policy.agentUse && <Badge title="Agent may not use">no agent</Badge>}
            </button>
          ))}
        </div>
      </aside>
      <section className="flex-1 min-w-0 overflow-y-auto">
        {sel ? (
          <ItemDetail key={sel.id} item={sel} onEdit={() => setEditItem(sel)} onChanged={() => void items.reload()} onDeleted={() => (setSelId(null), void items.reload())} />
        ) : (
          <EmptyState icon={<ShieldCheck />} title="Select an item" className="h-full">
            Secrets are encrypted at rest with your master password. Workspaces reference them as{" "}
            <span className="font-mono text-fg">vault://scope/name</span>; the agent can <em>use</em> them but never see them.
          </EmptyState>
        )}
      </section>
      <VaultItemModal open={createOpen} onClose={() => setCreateOpen(false)} scope={scope} scopes={scopes} onSaved={onSaved} />
      <VaultItemModal open={!!editItem} onClose={() => setEditItem(null)} scope={scope} item={editItem} onSaved={onSaved} />
      <GenerateKeyModal open={genOpen} onClose={() => setGenOpen(false)} scope={scope} scopes={scopes} onSaved={onSaved} />
      <ImportKeyModal open={importOpen} onClose={() => setImportOpen(false)} scope={scope} scopes={scopes} onSaved={onSaved} />
    </div>
  );
}

function ItemDetail({ item, onEdit, onChanged, onDeleted }: { item: Item; onEdit: () => void; onChanged: () => void; onDeleted: () => void }) {
  const { itemTypes } = useApp();
  const toast = useToast();
  const confirm = useConfirm();
  const def = itemTypes.find((t) => t.type === item.type);
  const [policyBusy, setPolicyBusy] = useState(false);
  const fieldsSorted = useMemo(() => {
    const order = def?.fields.map((f) => f.name) ?? [];
    return [...item.fields].sort((a, b) => {
      const ia = order.indexOf(a.name);
      const ib = order.indexOf(b.name);
      return (ia < 0 ? 99 : ia) - (ib < 0 ? 99 : ib);
    });
  }, [item, def]);
  const hasTotp = item.fields.some((f) => /totp/i.test(f.name) && f.hasValue);

  async function setPolicy(p: Partial<Item["policy"]>) {
    setPolicyBusy(true);
    try {
      await api.updateItem(item.id, {
        scope: item.scope,
        type: item.type,
        name: item.name,
        tags: item.tags,
        notes: item.notes,
        fields: item.fields.map((f) => ({ name: f.name, secret: f.secret, value: f.secret ? null : (f.value ?? "") })),
        policy: { ...item.policy, ...p },
      });
      onChanged();
    } catch (e) {
      toast.error("Update failed", errMsg(e));
    } finally {
      setPolicyBusy(false);
    }
  }

  async function remove() {
    const ok = await confirm({
      title: `Delete ${item.name}?`,
      message: "This permanently deletes the secret. Resources referencing it will fail to connect.",
      danger: true,
      confirmLabel: "Delete forever",
      typeToConfirm: item.name,
    });
    if (!ok) return;
    try {
      await api.deleteItem(item.id);
      toast.success("Item deleted", item.name);
      onDeleted();
    } catch (e) {
      toast.error("Delete failed", errMsg(e));
    }
  }

  return (
    <div className="max-w-3xl px-8 py-6">
      <div className="flex items-start gap-3">
        <span className="size-10 rounded-lg grid place-items-center bg-accent/15 text-accent shrink-0">
          <ItemTypeIcon type={item.type} className="size-5" />
        </span>
        <div className="min-w-0 flex-1">
          <h2 className="text-[18px] font-semibold font-mono truncate">{item.name}</h2>
          <div className="flex items-center gap-2 mt-0.5 text-xs text-muted">
            <span>{def?.label ?? item.type}</span>
            <span>·</span>
            <span className="font-mono">{item.ref}</span>
            <CopyButton text={item.ref} />
          </div>
        </div>
        <Button size="sm" icon={<Pencil className="size-3.5" />} onClick={onEdit}>
          Edit
        </Button>
        <Button size="sm" variant="danger" icon={<Trash2 className="size-3.5" />} onClick={remove}>
          Delete
        </Button>
      </div>
      {item.tags.length > 0 && (
        <div className="flex gap-1 mt-3">
          {item.tags.map((t) => (
            <Badge key={t}>{t}</Badge>
          ))}
        </div>
      )}

      {item.type === "ssh-key" && item.publicKey && (
        <div className="mt-5">
          <div className="flex items-center justify-between mb-1">
            <span className="text-xs font-medium text-muted">Public key</span>
            <CopyButton text={item.publicKey} label="Copy public key" />
          </div>
          <CodeBlock copy={false}>{item.publicKey}</CodeBlock>
          {item.fingerprint && <div className="mt-1 font-mono text-[11px] text-faint">{item.fingerprint}</div>}
        </div>
      )}

      {hasTotp && <TotpView id={item.id} />}

      <div className="mt-5 rounded-lg border border-line bg-panel divide-y divide-line">
        {fieldsSorted.map((f) => (
          <FieldRow key={f.name} item={item} f={f} label={def?.fields.find((d) => d.name === f.name)?.label ?? f.name} />
        ))}
        {fieldsSorted.length === 0 && <div className="p-3 text-xs text-faint">No fields</div>}
      </div>

      {item.notes && (
        <div className="mt-5">
          <div className="text-xs font-medium text-muted mb-1">Notes</div>
          <div className="rounded-md border border-line bg-panel p-3 whitespace-pre-wrap text-[13px]">{item.notes}</div>
        </div>
      )}

      <div className="mt-5 rounded-lg border border-line bg-panel p-3 space-y-2">
        <div className="text-xs font-medium text-muted">Access policy</div>
        <Toggle checked={item.policy.agentUse} disabled={policyBusy} onChange={(v) => void setPolicy({ agentUse: v })} label="Agent may use this item (it can never reveal it)" />
        <Toggle checked={item.policy.confirmOnUse} disabled={policyBusy} onChange={(v) => void setPolicy({ confirmOnUse: v })} label="Ask me before every use" />
      </div>

      <div className="mt-5 text-[11px] text-faint flex gap-4">
        <span>Created {fmtTime(item.created)}</span>
        <span>Updated {fmtTime(item.updated)}</span>
        <span>Revision {item.revision}</span>
      </div>
    </div>
  );
}

function FieldRow({ item, f, label }: { item: Item; f: Field; label: string }) {
  const [revealed, setRevealed] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const toast = useToast();
  useEffect(() => {
    if (revealed === null) return;
    const t = setTimeout(() => setRevealed(null), 30000);
    return () => clearTimeout(t);
  }, [revealed]);
  async function reveal() {
    if (revealed !== null) return setRevealed(null);
    setBusy(true);
    try {
      setRevealed((await api.reveal(item.id, f.name)).value);
    } catch (e) {
      toast.error("Reveal failed", errMsg(e));
    } finally {
      setBusy(false);
    }
  }
  const value = f.secret ? revealed : (f.value ?? "");
  const multiline = (value ?? "").includes("\n");
  return (
    <div className="flex items-start gap-3 px-3 py-2.5">
      <div className="w-36 shrink-0 pt-0.5">
        <div className="text-xs text-muted">{label}</div>
        {f.secret && <div className="text-[10px] text-faint uppercase tracking-wide">secret</div>}
      </div>
      <div className="flex-1 min-w-0">
        {!f.hasValue && !(f.value ?? "") ? (
          <span className="text-faint text-xs">—</span>
        ) : f.secret && revealed === null ? (
          <span className="font-mono text-muted tracking-widest">••••••••••••</span>
        ) : multiline ? (
          <pre className="font-mono text-[11.5px] whitespace-pre-wrap break-all max-h-60 overflow-auto rounded bg-panel-2 border border-line p-2">{value}</pre>
        ) : (
          <span className="font-mono text-[12.5px] break-all">{value}</span>
        )}
      </div>
      {(f.hasValue || f.value) && (
        <div className="flex items-center gap-0.5 shrink-0">
          {f.secret && (
            <IconButton title={revealed === null ? "Reveal (audited)" : "Hide"} onClick={reveal} disabled={busy} className="size-6">
              {revealed === null ? <Eye className="size-3.5" /> : <EyeOff className="size-3.5" />}
            </IconButton>
          )}
          <CopyButton
            getText={async () => (f.secret ? (revealed ?? (await api.reveal(item.id, f.name)).value) : (f.value ?? ""))}
            clearAfterMs={f.secret ? 20000 : undefined}
          />
        </div>
      )}
    </div>
  );
}

function TotpView({ id }: { id: string }) {
  const [code, setCode] = useState<{ code: string; period: number; remaining: number } | null>(null);
  const [err, setErr] = useState("");
  const load = () =>
    api
      .totp(id)
      .then((c) => {
        setCode(c);
        setErr("");
      })
      .catch((e) => setErr(errMsg(e)));
  useEffect(() => {
    void load();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [id]);
  useInterval(() => {
    setCode((c) => {
      if (!c) return c;
      if (c.remaining <= 1) {
        void load();
        return c;
      }
      return { ...c, remaining: c.remaining - 1 };
    });
  }, 1000);
  if (err) return <div className="mt-5 text-xs text-err">TOTP: {err}</div>;
  if (!code) return null;
  const pct = (code.remaining / (code.period || 30)) * 100;
  return (
    <div className="mt-5 flex items-center gap-4 rounded-lg border border-line bg-panel p-3">
      <div className="text-xs text-muted w-20">One-time code</div>
      <div className="font-mono text-[22px] tracking-[0.2em] tabular-nums">
        {code.code.slice(0, 3)} {code.code.slice(3)}
      </div>
      <CopyButton text={code.code} />
      <div className="flex-1" />
      <div className="relative size-8">
        <svg viewBox="0 0 36 36" className="size-8 -rotate-90">
          <circle cx="18" cy="18" r="15" fill="none" stroke="var(--c-panel-3)" strokeWidth="3" />
          <circle
            cx="18"
            cy="18"
            r="15"
            fill="none"
            stroke={code.remaining <= 5 ? "var(--c-err)" : "var(--c-accent)"}
            strokeWidth="3"
            strokeDasharray={`${(pct / 100) * 94.2} 94.2`}
          />
        </svg>
        <span className="absolute inset-0 grid place-items-center text-[10px] tabular-nums">{code.remaining}</span>
      </div>
    </div>
  );
}

function GenerateKeyModal({
  open,
  onClose,
  scope,
  scopes,
  onSaved,
}: {
  open: boolean;
  onClose: () => void;
  scope: string;
  scopes: { id: string; label: string }[];
  onSaved: (i: Item) => void;
}) {
  const [sc, setSc] = useState(scope);
  const [name, setName] = useState("");
  const [algorithm, setAlgorithm] = useState("ed25519");
  const [comment, setComment] = useState("");
  const [tags, setTags] = useState<string[]>([]);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const [result, setResult] = useState<Item | null>(null);
  useEffect(() => {
    if (open) {
      setSc(scope);
      setName("");
      setComment("");
      setTags([]);
      setErr("");
      setResult(null);
    }
  }, [open, scope]);
  async function submit() {
    setBusy(true);
    setErr("");
    try {
      const it = await api.sshGenerate({ scope: sc, name: name.trim(), algorithm, comment, tags });
      setResult(it);
      onSaved(it);
    } catch (e) {
      setErr(errMsg(e));
    } finally {
      setBusy(false);
    }
  }
  return (
    <Modal
      open={open}
      onClose={onClose}
      title="Generate SSH key"
      icon={<Sparkles />}
      width={560}
      footer={
        result ? (
          <Button variant="primary" onClick={onClose}>
            Done
          </Button>
        ) : (
          <>
            <Button variant="ghost" onClick={onClose}>
              Cancel
            </Button>
            <Button variant="primary" onClick={submit} loading={busy} disabled={!name.trim()}>
              Generate
            </Button>
          </>
        )
      }
    >
      {result ? (
        <div className="space-y-3">
          <div className="text-ok text-[13px]">Key generated and stored in the vault. The private key never leaves it.</div>
          <div>
            <div className="flex items-center justify-between mb-1">
              <span className="text-xs text-muted">Public key — add it to the server's authorized_keys</span>
              <CopyButton text={result.publicKey ?? ""} label="Copy" />
            </div>
            <CodeBlock copy={false}>{result.publicKey ?? ""}</CodeBlock>
            <div className="mt-1 font-mono text-[11px] text-faint">{result.fingerprint}</div>
          </div>
        </div>
      ) : (
        <div className="space-y-3">
          <div className="grid grid-cols-2 gap-3">
            <FormField label="Scope">
              <Select value={sc} onChange={(e) => setSc(e.target.value)}>
                {scopes.map((s) => (
                  <option key={s.id} value={s.id}>
                    {s.label}
                  </option>
                ))}
              </Select>
            </FormField>
            <FormField label="Algorithm">
              <Select value={algorithm} onChange={(e) => setAlgorithm(e.target.value)}>
                <option value="ed25519">Ed25519 (recommended)</option>
                <option value="rsa">RSA 4096</option>
                <option value="ecdsa">ECDSA P-256</option>
              </Select>
            </FormField>
          </div>
          <FormField label="Name" required>
            <Input mono autoFocus value={name} onChange={(e) => setName(e.target.value)} placeholder="deploy-key" />
          </FormField>
          <FormField label="Comment">
            <Input mono value={comment} onChange={(e) => setComment(e.target.value)} placeholder="deploy@acme" />
          </FormField>
          <FormField label="Tags">
            <ChipInput value={tags} onChange={setTags} />
          </FormField>
          {err && <div className="text-err text-xs">{err}</div>}
        </div>
      )}
    </Modal>
  );
}

function ImportKeyModal({
  open,
  onClose,
  scope,
  scopes,
  onSaved,
}: {
  open: boolean;
  onClose: () => void;
  scope: string;
  scopes: { id: string; label: string }[];
  onSaved: (i: Item) => void;
}) {
  const [sc, setSc] = useState(scope);
  const [name, setName] = useState("");
  const [key, setKey] = useState("");
  const [pass, setPass] = useState("");
  const [tags, setTags] = useState<string[]>([]);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  useEffect(() => {
    if (open) {
      setSc(scope);
      setName("");
      setKey("");
      setPass("");
      setTags([]);
      setErr("");
    }
  }, [open, scope]);
  async function submit() {
    setBusy(true);
    setErr("");
    try {
      const it = await api.sshImport({ scope: sc, name: name.trim(), privateKey: key, passphrase: pass, tags });
      onSaved(it);
      onClose();
    } catch (e) {
      setErr(errMsg(e));
    } finally {
      setBusy(false);
    }
  }
  return (
    <Modal
      open={open}
      onClose={onClose}
      title="Import SSH private key"
      icon={<FileUp />}
      width={600}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" onClick={submit} loading={busy} disabled={!name.trim() || !key.trim()}>
            Import
          </Button>
        </>
      }
    >
      <div className="space-y-3">
        <div className="grid grid-cols-2 gap-3">
          <FormField label="Scope">
            <Select value={sc} onChange={(e) => setSc(e.target.value)}>
              {scopes.map((s) => (
                <option key={s.id} value={s.id}>
                  {s.label}
                </option>
              ))}
            </Select>
          </FormField>
          <FormField label="Name" required>
            <Input mono autoFocus value={name} onChange={(e) => setName(e.target.value)} placeholder="id_ed25519" />
          </FormField>
        </div>
        <div>
          <div className="flex items-center justify-between mb-1">
            <span className="text-xs font-medium text-muted">Private key (OpenSSH, PEM or PuTTY .ppk)</span>
            <label className="text-xs text-accent cursor-pointer hover:underline">
              Load file…
              <input
                type="file"
                className="hidden"
                onChange={async (e) => {
                  const f = e.target.files?.[0];
                  if (f) {
                    setKey(await f.text());
                    if (!name) setName(f.name.replace(/\.(pem|ppk|key)$/i, ""));
                  }
                  e.target.value = "";
                }}
              />
            </label>
          </div>
          <Textarea mono value={key} onChange={(e) => setKey(e.target.value)} className="min-h-40" placeholder="-----BEGIN OPENSSH PRIVATE KEY-----" spellCheck={false} />
          <div className="text-[11px] text-faint mt-1">Read locally in your browser and sent only to your local opsdeck server.</div>
        </div>
        <FormField label="Passphrase (if encrypted)">
          <Input type="password" value={pass} onChange={(e) => setPass(e.target.value)} autoComplete="off" />
        </FormField>
        <FormField label="Tags">
          <ChipInput value={tags} onChange={setTags} />
        </FormField>
        {err && <div className="text-err text-xs">{err}</div>}
      </div>
    </Modal>
  );
}
