import { cx } from "./ui";

export function DiffView({ text }: { text: string }) {
  if (!text.trim()) return <div className="p-6 text-muted text-sm">No differences.</div>;
  return (
    <div className="font-mono text-[12px] leading-[1.55] py-2" dir="ltr">
      {text.split("\n").map((l, i) => {
        const file = l.startsWith("diff --git");
        const cls = file
          ? "mt-3 first:mt-0 border-y border-line bg-panel-2 text-fg font-semibold py-1"
          : l.startsWith("+++") || l.startsWith("---") || l.startsWith("index ") || l.startsWith("new file") || l.startsWith("deleted file") || l.startsWith("similarity") || l.startsWith("rename ")
            ? "text-faint"
            : l.startsWith("@@")
              ? "text-info bg-info/5"
              : l.startsWith("+")
                ? "text-ok bg-ok/8"
                : l.startsWith("-")
                  ? "text-err bg-err/8"
                  : "text-muted";
        return (
          <div key={i} className={cx("px-4 whitespace-pre-wrap break-all", cls)}>
            {file ? l.replace(/^diff --git a\/(.*) b\/.*$/, "$1") : l || " "}
          </div>
        );
      })}
    </div>
  );
}
