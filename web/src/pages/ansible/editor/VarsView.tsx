import { Variable } from "lucide-react";
import { useEditor } from "./context";
import { SectionTitle, VarsTable } from "./fields";
import { applyRows, ensureMap, getMap, rowsOf } from "./model";

/** VarsView edits a variables file (group_vars, host_vars, vars, defaults, a decrypted vault file). */
export function VarsView() {
  const { doc, edit, setSecret, readOnly } = useEditor();
  const rows = rowsOf(getMap(doc, []));
  return (
    <div className="space-y-2">
      <SectionTitle icon={<Variable />} count={rows.length}>
        Variables
      </SectionTitle>
      <VarsTable
        rows={rows}
        onChange={(r) => !readOnly && edit((d) => applyRows(d, ensureMap(d, []), r))}
        onVault={setSecret}
        empty="No variables yet: add one below."
      />
      <p className="text-[11px] text-faint">Values can use Jinja ({"{{ other_var }}"}). Lists and maps are edited as YAML (type “list/map”).</p>
    </div>
  );
}
