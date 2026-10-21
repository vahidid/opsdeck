import { useEffect, useMemo, useRef, useState } from "react";
import { Dices, Eye, EyeOff, Upload } from "lucide-react";
import { api, errMsg } from "../api/client";
import type { Item, ItemTypeField } from "../api/types";
import { useApp } from "../context/app";
import { Button, ChipInput, FormField, IconButton, Input, Label, Select, Textarea, Toggle } from "./ui";
import { Modal } from "./overlay";
import { ItemTypeIcon } from "./icons";

export function VaultItemModal({
  open,
  onClose,
  scope,
  scopes,
  defaultType,
  allowedTypes,
  item,
  onSaved,
}: {
  open: boolean;
  onClose: () => void;
  scope: string;
  scopes?: { id: string; label: string }[];
  defaultType?: string;
  allowedTypes?: string[];
  item?: Item | null;
  onSaved: (item: Item) => void;
}) {
  const { itemTypes } = useApp();
  const types = useMemo(
    () => (allowedTypes && allowedTypes.length ? itemTypes.filter((t) => allowedTypes.includes(t.type)) : itemTypes),
    [itemTypes, allowedTypes],
  );
  const [type, setType] = useState("");
  const [sc, setSc] = useState(scope);
  const [name, setName] = useState("");
  const [tags, setTags] = useState<string[]>([]);
  const [notes, setNotes] = useState("");
  const [values, setValues] = useState<Record<string, string>>({});
  const [touched, setTouched] = useState<Record<string, boolean>>({});
  const [agentUse, setAgentUse] = useState(true);
  const [confirmOnUse, setConfirmOnUse] = useState(false);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");

  useEffect(() => {
    if (!open) return;
    setErr("");
    setTouched({});
    if (item) {
      setType(item.type);
      setSc(item.scope);
      setName(item.name);
      setTags(item.tags ?? []);
      setNotes(item.notes ?? "");
      setValues(Object.fromEntries(item.fields.filter((f) => !f.secret).map((f) => [f.name, f.value ?? ""])));
      setAgentUse(item.policy?.agentUse ?? true);
      setConfirmOnUse(item.policy?.confirmOnUse ?? false);
    } else {
      setType(defaultType && types.some((t) => t.type === defaultType) ? defaultType : (types[0]?.type ?? ""));
      setSc(scope);
      setName("");
      setTags([]);
      setNotes("");
      setValues({});
      setAgentUse(true);
      setConfirmOnUse(false);
    }
  }, [open, item, defaultType, scope, types]);

  const def = itemTypes.find((t) => t.type === type);
  const editing = !!item;

  function existing(f: ItemTypeField) {
    return item?.fields.find((x) => x.name === f.name);
  }

  async function submit() {
    if (!def) return;
    setBusy(true);
    setErr("");
    try {
      const fields: { name: string; value?: string | null; secret: boolean }[] = def.fields.map((f) => {
        if (editing && f.secret && !touched[f.name]) return { name: f.name, value: null, secret: true };
        return { name: f.name, value: values[f.name] ?? "", secret: f.secret };
      });
      // keep fields that exist on the item but are not part of the type definition
      if (item) {
        for (const f of item.fields) {
          if (!def.fields.some((d) => d.name === f.name)) fields.push({ name: f.name, value: f.secret ? null : (f.value ?? ""), secret: f.secret });
        }
      }
      const input = { scope: sc, type, name: name.trim(), tags, notes, fields, policy: { agentUse, confirmOnUse } };
      const saved = item ? await api.updateItem(item.id, input) : await api.createItem(input);
      onSaved(saved);
      onClose();
    } catch (e) {
      setErr(errMsg(e));
    } finally {
      setBusy(false);
    }
  }

  const missing = def?.fields.some((f) => f.required && !(editing && f.secret && !touched[f.name] && existing(f)?.hasValue) && !(values[f.name] ?? "").trim());

  return (
    <Modal
      open={open}
      onClose={onClose}
      width={600}
      icon={def ? <ItemTypeIcon type={def.type} /> : undefined}
      title={editing ? `Edit ${item?.name}` : "New vault item"}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" onClick={submit} loading={busy} disabled={!name.trim() || !def || !!missing}>
            {editing ? "Save" : "Create"}
          </Button>
        </>
      }
    >
      <div className="space-y-3">
        <div className="grid grid-cols-2 gap-3">
          <FormField label="Type">
            <Select value={type} onChange={(e) => setType(e.target.value)} disabled={editing}>
              {(editing ? itemTypes : types).map((t) => (
                <option key={t.type} value={t.type}>
                  {t.label}
                </option>
              ))}
            </Select>
          </FormField>
          <FormField label="Scope">
            <Select value={sc} onChange={(e) => setSc(e.target.value)} disabled={editing || !scopes}>
              {(scopes ?? [{ id: scope, label: scope }]).map((s) => (
                <option key={s.id} value={s.id}>
                  {s.label}
                </option>
              ))}
            </Select>
          </FormField>
        </div>
        <FormField label="Name" required help={<span className="font-mono">vault://{sc}/{name || "<name>"}</span>}>
          <Input mono autoFocus value={name} onChange={(e) => setName(e.target.value)} placeholder="deploy-key" />
        </FormField>
        {def?.fields.map((f) => (
          <SecretField
            key={`${type}:${f.name}`}
            def={f}
            value={values[f.name] ?? ""}
            hasExisting={editing && f.secret && !!existing(f)?.hasValue}
            touched={!!touched[f.name]}
            onChange={(v) => {
              setValues((s) => ({ ...s, [f.name]: v }));
              setTouched((t) => ({ ...t, [f.name]: true }));
            }}
          />
        ))}
        <FormField label="Tags">
          <ChipInput value={tags} onChange={setTags} placeholder="prod, deploy…" />
        </FormField>
        <FormField label="Notes">
          <Textarea value={notes} onChange={(e) => setNotes(e.target.value)} className="min-h-14" />
        </FormField>
        <div className="rounded-md border border-line bg-panel-2 p-3 space-y-2">
          <div className="text-xs font-medium text-muted">Access policy</div>
          <Toggle checked={agentUse} onChange={setAgentUse} label="Agent may use this item (never reveal)" />
          <Toggle checked={confirmOnUse} onChange={setConfirmOnUse} label="Ask me before every use" />
        </div>
        {err && <div className="text-err text-xs">{err}</div>}
      </div>
    </Modal>
  );
}

function SecretField({
  def,
  value,
  onChange,
  hasExisting,
  touched,
}: {
  def: ItemTypeField;
  value: string;
  onChange: (v: string) => void;
  hasExisting: boolean;
  touched: boolean;
}) {
  const [show, setShow] = useState(false);
  const fileRef = useRef<HTMLInputElement>(null);
  const isPasswordish = /pass|secret|password/i.test(def.name) && !def.multiline;
  const placeholder = hasExisting && !touched ? "•••••••• (unchanged — type to replace)" : "";

  async function gen() {
    try {
      const r = await api.generatePassword(24, true);
      onChange(r.password);
      setShow(true);
    } catch {
      /* ignore */
    }
  }

  return (
    <div>
      <Label required={def.required && !hasExisting} hint={def.secret ? "secret" : undefined}>
        {def.label}
      </Label>
      {def.multiline ? (
        <div className="relative">
          <Textarea
            mono
            value={value}
            onChange={(e) => onChange(e.target.value)}
            placeholder={placeholder || (def.secret ? "Paste or load from file" : "")}
            className="min-h-28 pr-9"
            style={def.secret && !show && value ? ({ WebkitTextSecurity: "disc" } as React.CSSProperties) : undefined}
            spellCheck={false}
          />
          <div className="absolute right-1 top-1 flex flex-col gap-0.5">
            <IconButton title="Load from file" type="button" onClick={() => fileRef.current?.click()}>
              <Upload className="size-3.5" />
            </IconButton>
            {def.secret && (
              <IconButton title={show ? "Hide" : "Show"} type="button" onClick={() => setShow((s) => !s)}>
                {show ? <EyeOff className="size-3.5" /> : <Eye className="size-3.5" />}
              </IconButton>
            )}
          </div>
          <input
            ref={fileRef}
            type="file"
            className="hidden"
            onChange={async (e) => {
              const f = e.target.files?.[0];
              if (f) onChange(await f.text());
              e.target.value = "";
            }}
          />
        </div>
      ) : (
        <div className="flex gap-1">
          <Input
            mono={def.secret}
            type={def.secret && !show ? "password" : "text"}
            value={value}
            onChange={(e) => onChange(e.target.value)}
            placeholder={placeholder}
            autoComplete="new-password"
            spellCheck={false}
          />
          {def.secret && (
            <IconButton title={show ? "Hide" : "Show"} type="button" onClick={() => setShow((s) => !s)} className="size-8">
              {show ? <EyeOff className="size-4" /> : <Eye className="size-4" />}
            </IconButton>
          )}
          {isPasswordish && (
            <IconButton title="Generate strong password" type="button" onClick={gen} className="size-8">
              <Dices className="size-4" />
            </IconButton>
          )}
        </div>
      )}
    </div>
  );
}

/** Picker for a `vault://` reference. Lists global + workspace items filtered by type. */
export function SecretRefPicker({
  value,
  onChange,
  ws,
  itemTypes,
  placeholder = "— none —",
}: {
  value: string;
  onChange: (v: string) => void;
  ws: string;
  itemTypes?: string[];
  placeholder?: string;
}) {
  const [items, setItems] = useState<Item[]>([]);
  const [createOpen, setCreateOpen] = useState(false);
  const [err, setErr] = useState("");
  const load = async () => {
    try {
      const [g, w] = await Promise.all([api.items("global"), api.items(ws)]);
      setItems([...w, ...g]);
      setErr("");
    } catch (e) {
      setErr(errMsg(e));
    }
  };
  useEffect(() => {
    void load();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ws]);
  const filtered = items.filter((i) => !itemTypes?.length || itemTypes.includes(i.type));
  const known = filtered.some((i) => i.ref === value || value.startsWith(i.ref + "#"));
  return (
    <div>
      <div className="flex gap-1">
        <Select value={value} onChange={(e) => (e.target.value === "__new" ? setCreateOpen(true) : onChange(e.target.value))} className="font-mono text-xs">
          <option value="">{placeholder}</option>
          {value && !known && <option value={value}>{value} (not found)</option>}
          {[ws, "global"].map((sc) => {
            const list = filtered.filter((i) => i.scope === sc);
            if (!list.length) return null;
            return (
              <optgroup key={sc} label={sc === "global" ? "Global" : `Workspace: ${sc}`}>
                {list.map((i) => (
                  <option key={i.id} value={i.ref}>
                    {i.name} · {i.type}
                  </option>
                ))}
              </optgroup>
            );
          })}
          <option value="__new">+ Create new item…</option>
        </Select>
      </div>
      {err && <div className="text-[11px] text-err mt-1">{err}</div>}
      <VaultItemModal
        open={createOpen}
        onClose={() => setCreateOpen(false)}
        scope={ws}
        scopes={[
          { id: ws, label: `Workspace: ${ws}` },
          { id: "global", label: "Global" },
        ]}
        allowedTypes={itemTypes}
        defaultType={itemTypes?.[0]}
        onSaved={(it) => {
          void load();
          onChange(it.ref);
        }}
      />
    </div>
  );
}
