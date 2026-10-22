import { useMemo, useState } from "react";
import { ChevronDown, ChevronRight, ListTree, RefreshCw, Server } from "lucide-react";
import { api, errMsg } from "../../api/client";
import { Modal, useToast } from "../../components/overlay";
import { Badge, Button, CodeBlock, EmptyState, ErrorBox, IconButton, Loading, SearchInput, Select, cx } from "../../components/ui";
import { useAsync } from "../../hooks";
import { inventoryLabel } from "./Run";
import { Section, useAnsible } from "./shared";

// InventoryTab shows an inventory's groups and hosts (ansible-inventory) and
// a host's variables on request.
export function InventoryTab() {
  const { ws, project: p, withPassword } = useAnsible();
  const toast = useToast();
  const options = Array.from(new Set([...(p.inventory ? [p.inventory] : []), ...p.inventories, "workspace"]));
  const [inv, setInv] = useState(options[0] ?? "workspace");
  const data = useAsync(() => withPassword(() => api.ansibleInventory(ws, p.name, inv)).then((r) => r ?? null), [ws, p.name, inv]);
  const [q, setQ] = useState("");
  const [vars, setVars] = useState<{ host: string; text: string } | null>(null);
  const [closed, setClosed] = useState<Record<string, boolean>>({});
  const d = data.data;

  const hostGroups = useMemo(() => {
    const m: Record<string, string[]> = {};
    for (const [g, hs] of Object.entries(d?.groups ?? {})) for (const h of hs) (m[h] ??= []).push(g);
    return m;
  }, [d]);

  async function showVars(host: string) {
    try {
      const r = await withPassword(() => api.ansibleHostVars(ws, p.name, inv, host));
      if (r) setVars({ host, text: r.vars });
    } catch (e) {
      toast.error("Could not read the variables", errMsg(e));
    }
  }

  // the group tree, from "all" down
  const renderGroup = (g: string, depth: number, seen: Set<string>): React.ReactNode => {
    if (seen.has(g)) return null;
    const next = new Set(seen).add(g);
    const kids = d?.children[g] ?? [];
    const hosts = (d?.groups[g] ?? []).filter((h) => !q || h.toLowerCase().includes(q.toLowerCase()));
    const open = !closed[g];
    return (
      <div key={g + depth}>
        <button
          onClick={() => setClosed((c) => ({ ...c, [g]: open }))}
          className="flex items-center gap-1.5 w-full text-left py-0.5 hover:text-fg text-muted"
          style={{ paddingInlineStart: depth * 14 }}
        >
          {open ? <ChevronDown className="size-3" /> : <ChevronRight className="size-3" />}
          <span className="font-mono text-[12.5px] text-fg">@{g}</span>
          {hosts.length > 0 && <span className="text-[11px] text-faint">{hosts.length}</span>}
        </button>
        {open && (
          <>
            {kids.map((k) => renderGroup(k, depth + 1, next))}
            {hosts.map((h) => (
              <button
                key={g + "/" + h}
                onClick={() => void showVars(h)}
                className="flex items-center gap-1.5 w-full text-left py-0.5 rounded hover:bg-panel-2"
                style={{ paddingInlineStart: (depth + 1) * 14 + 12 }}
                title="Show the host's variables"
              >
                <Server className="size-3 text-faint" />
                <span className="font-mono text-[12px]">{h}</span>
              </button>
            ))}
          </>
        )}
      </div>
    );
  };

  return (
    <div className="flex flex-col h-full min-h-0 gap-3">
      <div className="flex items-center gap-2">
        <Select value={inv} onChange={(e) => setInv(e.target.value)} className="w-80">
          {options.map((o) => (
            <option key={o} value={o}>
              {inventoryLabel(o)}
            </option>
          ))}
        </Select>
        <div className="w-64">
          <SearchInput value={q} onChange={setQ} placeholder="Filter hosts…" />
        </div>
        <IconButton title="Reload" onClick={() => void data.reload()}>
          <RefreshCw className={cx("size-4", data.loading && "animate-spin")} />
        </IconButton>
        {d && <span className="text-xs text-muted">{d.hosts.length} hosts · {Object.keys(d.groups).length} groups</span>}
      </div>
      {!d && data.loading ? (
        <Loading />
      ) : d === null ? (
        <EmptyState icon={<ListTree />} title="Locked" action={<Button onClick={() => void data.reload()}>Unlock</Button>}>
          The inventory reads encrypted variables; it needs the vault password.
        </EmptyState>
      ) : data.error ? (
        <ErrorBox error={data.error} onRetry={() => void data.reload()} />
      ) : d?.error ? (
        <div className="space-y-2">
          <ErrorBox error={d.error} onRetry={() => void data.reload()} />
          {d.graph && <CodeBlock maxHeight={400}>{d.graph}</CodeBlock>}
        </div>
      ) : d && d.hosts.length === 0 ? (
        <EmptyState icon={<ListTree />} title="No hosts in this inventory" />
      ) : (
        d && (
          <div className="flex-1 min-h-0 grid grid-cols-1 lg:grid-cols-2 gap-3">
            <Section title="Groups" className="flex flex-col min-h-0">
              <div className="flex-1 overflow-y-auto p-3">{renderGroup("all", 0, new Set())}</div>
            </Section>
            <Section title="Hosts" className="flex flex-col min-h-0">
              <div className="flex-1 overflow-y-auto divide-y divide-line">
                {d.hosts
                  .filter((h) => !q || h.toLowerCase().includes(q.toLowerCase()))
                  .map((h) => (
                    <button key={h} onClick={() => void showVars(h)} className="w-full text-left px-3 py-1.5 hover:bg-panel-2 flex items-center gap-2">
                      <Server className="size-3.5 text-faint" />
                      <span className="font-mono text-[12.5px] flex-1">{h}</span>
                      <span className="flex flex-wrap gap-1 justify-end">
                        {(hostGroups[h] ?? [])
                          .filter((g) => g !== "all" && g !== "ungrouped")
                          .map((g) => (
                            <Badge key={g} mono>
                              {g}
                            </Badge>
                          ))}
                      </span>
                    </button>
                  ))}
              </div>
              {d.generated && (
                <details className="border-t border-line px-3 py-2">
                  <summary className="text-xs text-muted cursor-pointer select-none">Generated inventory</summary>
                  <CodeBlock className="mt-2" maxHeight={300}>
                    {d.generated}
                  </CodeBlock>
                </details>
              )}
            </Section>
          </div>
        )
      )}
      <Modal open={!!vars} onClose={() => setVars(null)} title={vars ? `Variables of ${vars.host}` : ""} width={720} icon={<Server />}>
        {vars && (
          <div className="space-y-2">
            <div className="text-xs text-muted">From ansible-inventory: inventory, group_vars and host_vars, vault values decrypted.</div>
            <CodeBlock maxHeight={560}>{vars.text.trim()}</CodeBlock>
          </div>
        )}
      </Modal>
    </div>
  );
}
