import { useState } from "react";
import { BookOpen, Package, Plus, Trash2 } from "lucide-react";
import { Button, IconButton, Input } from "../../../components/ui";
import { useAnsible } from "../shared";
import { useEditor } from "./context";
import { SectionTitle } from "./fields";
import { ensureSeq, getSeq, toNode } from "./model";

type Req = Record<string, unknown>;

/** RequirementsView edits requirements.yml: the collections and roles ansible-galaxy installs. */
export function RequirementsView() {
  const { goto } = useAnsible();
  return (
    <div className="space-y-5">
      <ReqList k="collections" icon={<Package />} title="Collections" placeholder="community.general" />
      <ReqList k="roles" icon={<BookOpen />} title="Roles" placeholder="geerlingguy.docker" />
      <Button size="sm" variant="ghost" icon={<Package className="size-3.5" />} onClick={() => goto("run")}>
        Install them: Run → Galaxy
      </Button>
    </div>
  );
}

function ReqList({ k, icon, title, placeholder }: { k: string; icon: React.ReactNode; title: string; placeholder: string }) {
  const { doc, edit, readOnly } = useEditor();
  const items = ((getSeq(doc, [k])?.toJSON() ?? []) as unknown[]).map((x) => (typeof x === "string" ? { name: x } : ((x ?? {}) as Req)));
  const [name, setName] = useState("");
  const set = (i: number, patch: Req) =>
    edit((d) => {
      const s = getSeq(d, [k]);
      if (!s) return;
      const next = { ...items[i], ...patch };
      for (const key of Object.keys(next)) if (next[key] === "" || next[key] === undefined) delete next[key];
      s.items[i] = toNode(d, Object.keys(next).length === 1 && next.name ? next.name : next) as never;
    });
  const nameKey = (r: Req) => (r.name !== undefined ? "name" : r.src !== undefined ? "src" : "name");
  const srcKey = k === "roles" ? "src" : "source";
  return (
    <div>
      <SectionTitle icon={icon} count={items.length}>
        {title}
      </SectionTitle>
      <div className="rounded-md border border-line divide-y divide-line">
        <div className="grid grid-cols-[1fr_140px_1fr_32px] gap-2 px-2 py-1 text-[11px] text-faint">
          <span>name</span>
          <span>version</span>
          <span>source</span>
          <span />
        </div>
        {items.length === 0 && <div className="px-3 py-2 text-xs text-faint">None.</div>}
        {items.map((r, i) => (
          <div key={i} className="grid grid-cols-[1fr_140px_1fr_32px] gap-2 px-2 py-1.5 items-center">
            <Input mono value={String(r[nameKey(r)] ?? "")} disabled={readOnly} onChange={(e) => set(i, { [nameKey(r)]: e.target.value })} />
            <Input mono value={String(r.version ?? "")} disabled={readOnly} onChange={(e) => set(i, { version: e.target.value })} placeholder="latest" />
            <Input mono value={nameKey(r) === srcKey ? "" : String(r[srcKey] ?? "")} disabled={readOnly || nameKey(r) === srcKey} onChange={(e) => set(i, { [srcKey]: e.target.value })} placeholder="Galaxy" />
            {!readOnly && (
              <IconButton title="Remove" onClick={() => edit((d) => getSeq(d, [k])?.items.splice(i, 1))}>
                <Trash2 className="size-3.5" />
              </IconButton>
            )}
          </div>
        ))}
      </div>
      {!readOnly && (
        <div className="flex gap-1 mt-2 max-w-md">
          <Input
            mono
            value={name}
            onChange={(e) => setName(e.target.value.trim())}
            placeholder={placeholder}
            onKeyDown={(e) => {
              if (e.key === "Enter" && name) {
                edit((d) => ensureSeq(d, [k]).items.push(toNode(d, { name })));
                setName("");
              }
            }}
          />
          <Button
            icon={<Plus className="size-3.5" />}
            disabled={!name}
            onClick={() => {
              edit((d) => ensureSeq(d, [k]).items.push(toNode(d, { name })));
              setName("");
            }}
          >
            Add
          </Button>
        </div>
      )}
    </div>
  );
}
