import { useEffect, useState } from "react";
import type { ReactNode } from "react";
import { Braces, Eye, EyeOff, Lock, Plus, Trash2 } from "lucide-react";
import { Badge, IconButton, Input, Select, Textarea, cx } from "../../../components/ui";
import { parseValue, yamlText } from "./model";
import type { VarRow } from "./model";

/** a list of short strings (tags, notify, hosts…) as removable chips */
export function ChipsField({ value, onChange, placeholder, suggestions }: { value: string[]; onChange: (v: string[]) => void; placeholder?: string; suggestions?: string[] }) {
  const [draft, setDraft] = useState("");
  const id = useState(() => "chips-" + Math.random().toString(36).slice(2))[0];
  const add = () => {
    const parts = draft.split(",").map((s) => s.trim()).filter(Boolean);
    if (parts.length) onChange([...value, ...parts.filter((p) => !value.includes(p))]);
    setDraft("");
  };
  return (
    <div className="flex flex-wrap items-center gap-1 rounded-md border border-line bg-panel-2 px-1.5 py-1 min-h-8">
      {value.map((v) => (
        <span key={v} className="inline-flex items-center gap-1 rounded bg-panel-3 border border-line px-1.5 h-5 text-[11.5px] font-mono">
          {v}
          <button type="button" className="text-faint hover:text-err" onClick={() => onChange(value.filter((x) => x !== v))}>
            ×
          </button>
        </span>
      ))}
      <input
        list={suggestions?.length ? id : undefined}
        className="flex-1 min-w-24 bg-transparent outline-none text-[12.5px] font-mono px-1"
        value={draft}
        placeholder={value.length ? "" : placeholder}
        onChange={(e) => setDraft(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === "Enter" || e.key === ",") {
            e.preventDefault();
            add();
          } else if (e.key === "Backspace" && !draft && value.length) onChange(value.slice(0, -1));
        }}
        onBlur={add}
      />
      {suggestions?.length ? (
        <datalist id={id}>
          {suggestions.map((s) => (
            <option key={s} value={s} />
          ))}
        </datalist>
      ) : null}
    </div>
  );
}

/** a YAML value typed as text; reports the parsed value or an error */
export function YamlField({ value, onChange, rows = 4, placeholder }: { value: unknown; onChange: (v: unknown, error?: string) => void; rows?: number; placeholder?: string }) {
  const [text, setText] = useState(() => yamlText(value));
  const [err, setErr] = useState("");
  useEffect(() => {
    // follow outside changes unless the text already means the same
    const p = parseValue(text);
    if (!p.error && JSON.stringify(p.value) !== JSON.stringify(value)) setText(yamlText(value));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [JSON.stringify(value)]);
  return (
    <div>
      <Textarea
        mono
        rows={rows}
        spellCheck={false}
        value={text}
        placeholder={placeholder}
        dir="ltr"
        className="text-[12px]"
        onChange={(e) => {
          setText(e.target.value);
          const p = parseValue(e.target.value);
          setErr(p.error ?? "");
          onChange(p.value, p.error);
        }}
      />
      {err && <div className="text-[11px] text-err mt-0.5">{err}</div>}
    </div>
  );
}

type Kind = "string" | "number" | "bool" | "yaml";

function kindOf(v: unknown): Kind {
  if (typeof v === "boolean") return "bool";
  if (typeof v === "number") return "number";
  if (v !== null && typeof v === "object") return "yaml";
  return "string";
}

function coerce(v: unknown, k: Kind): unknown {
  switch (k) {
    case "bool":
      return v === true || v === "true" || v === "yes";
    case "number": {
      const n = Number(v);
      return isNaN(n) ? 0 : n;
    }
    case "string":
      return v === null || v === undefined ? "" : typeof v === "object" ? JSON.stringify(v) : String(v);
  }
  return v;
}

const SECRET_NAME = /pass|secret|token|api_?key|private|credential/i;

/** an editor for one value: text, number, true/false, or YAML for lists and maps; secret-looking text is masked until shown */
export function ValueEditor({ value, onChange, kind, onKind, secret }: { value: unknown; onChange: (v: unknown) => void; kind: Kind; onKind?: (k: Kind) => void; secret?: boolean }) {
  const [show, setShow] = useState(false);
  return (
    <div className="flex items-start gap-1.5 min-w-0">
      <div className="flex-1 min-w-0">
        {kind === "bool" ? (
          <Select value={value ? "true" : "false"} onChange={(e) => onChange(e.target.value === "true")}>
            <option value="true">true</option>
            <option value="false">false</option>
          </Select>
        ) : kind === "number" ? (
          <Input mono type="number" value={String(value ?? "")} onChange={(e) => onChange(e.target.value === "" ? 0 : Number(e.target.value))} />
        ) : kind === "yaml" ? (
          <YamlField value={value} onChange={(v, err) => !err && onChange(v ?? null)} rows={Math.min(10, Math.max(2, yamlText(value).split("\n").length))} />
        ) : String(value ?? "").includes("\n") ? (
          <Textarea mono rows={Math.min(10, String(value).split("\n").length + 1)} value={String(value ?? "")} onChange={(e) => onChange(e.target.value)} className="text-[12px]" dir="ltr" />
        ) : secret ? (
          <div className="flex items-center gap-1">
            <Input mono type={show ? "text" : "password"} autoComplete="off" value={String(value ?? "")} onChange={(e) => onChange(e.target.value)} dir="ltr" />
            <IconButton title={show ? "Hide" : "Show"} onClick={() => setShow(!show)}>
              {show ? <EyeOff className="size-3.5" /> : <Eye className="size-3.5" />}
            </IconButton>
          </div>
        ) : (
          <Input mono value={String(value ?? "")} onChange={(e) => onChange(e.target.value)} dir="ltr" />
        )}
      </div>
      {onKind && (
        <Select value={kind} onChange={(e) => onKind(e.target.value as Kind)} className="w-[104px] shrink-0 text-[11.5px]" title="Type">
          <option value="string">text</option>
          <option value="number">number</option>
          <option value="bool">bool</option>
          <option value="yaml">list/map</option>
        </Select>
      )}
    </div>
  );
}


/**
 * VarsTable edits a mapping of variables: one row per variable, typed
 * values, lists and maps as YAML; encrypted (!vault) values are shown
 * locked and changed through onVault.
 */
export function VarsTable({
  rows,
  onChange,
  onVault,
  empty = "No variables.",
  dense,
}: {
  rows: VarRow[];
  onChange: (rows: VarRow[]) => void;
  onVault?: (key: string | null) => void;
  empty?: ReactNode;
  dense?: boolean;
}) {
  const [kinds, setKinds] = useState<Record<string, Kind>>({});
  const [newKey, setNewKey] = useState("");
  const keyOk = /^[A-Za-z_][A-Za-z0-9_]*$/.test(newKey) && !rows.some((r) => r.key === newKey);
  const set = (i: number, patch: Partial<VarRow>) => onChange(rows.map((r, j) => (j === i ? { ...r, ...patch } : r)));
  return (
    <div className={cx("divide-y divide-line", !dense && "rounded-md border border-line")}>
      {rows.length === 0 && <div className="px-3 py-2 text-xs text-faint">{empty}</div>}
      {rows.map((r, i) => {
        const kind = kinds[r.key] ?? kindOf(r.value);
        return (
          <div key={i} className="flex items-start gap-2 px-2 py-1.5">
            <Input
              mono
              value={r.key}
              onChange={(e) => set(i, { key: e.target.value })}
              className="w-56 shrink-0 text-[12.5px] text-info"
              dir="ltr"
            />
            {r.vault ? (
              <div className="flex-1 flex items-center gap-2 h-8 text-xs text-warn">
                <Lock className="size-3.5" /> encrypted with Ansible Vault
                {onVault && (
                  <button className="text-accent hover:underline" onClick={() => onVault(r.key)}>
                    change…
                  </button>
                )}
              </div>
            ) : (
              <div className="flex-1 min-w-0">
                <ValueEditor
                  value={r.value}
                  kind={kind}
                  secret={SECRET_NAME.test(r.key) && typeof r.value === "string" && !String(r.value).includes("{{")}
                  onChange={(v) => set(i, { value: v })}
                  onKind={(k) => {
                    setKinds({ ...kinds, [r.key]: k });
                    set(i, { value: coerce(r.value, k) });
                  }}
                />
              </div>
            )}
            <IconButton title="Remove" onClick={() => onChange(rows.filter((_, j) => j !== i))}>
              <Trash2 className="size-3.5" />
            </IconButton>
          </div>
        );
      })}
      <div className="flex items-center gap-2 px-2 py-1.5">
        <Input
          mono
          value={newKey}
          onChange={(e) => setNewKey(e.target.value.trim())}
          placeholder="new_variable"
          className="w-56 text-[12.5px]"
          onKeyDown={(e) => {
            if (e.key === "Enter" && keyOk) {
              onChange([...rows, { key: newKey, value: "" }]);
              setNewKey("");
            }
          }}
        />
        <IconButton
          title="Add variable"
          disabled={!keyOk}
          onClick={() => {
            onChange([...rows, { key: newKey, value: "" }]);
            setNewKey("");
          }}
        >
          <Plus className="size-4" />
        </IconButton>
        {onVault && (
          <button className="ml-auto inline-flex items-center gap-1 text-xs text-warn hover:underline" onClick={() => onVault(null)}>
            <Lock className="size-3" /> Add encrypted variable
          </button>
        )}
      </div>
    </div>
  );
}

export function Pill({ children, tone = "neutral", title }: { children: ReactNode; tone?: "neutral" | "info" | "warn" | "accent" | "ok" | "err"; title?: string }) {
  return (
    <Badge tone={tone} title={title} className="max-w-[260px] truncate">
      {children}
    </Badge>
  );
}

export function SectionTitle({ icon, children, count, actions }: { icon?: ReactNode; children: ReactNode; count?: number; actions?: ReactNode }) {
  return (
    <div className="flex items-center gap-2 h-8">
      <span className="text-faint [&_svg]:size-3.5">{icon ?? <Braces />}</span>
      <span className="text-[12px] font-medium uppercase tracking-wide text-muted">{children}</span>
      {count !== undefined && <span className="text-[11px] text-faint tabular-nums">{count}</span>}
      <div className="flex-1" />
      {actions}
    </div>
  );
}
