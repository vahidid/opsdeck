export function CodeView({ text }: { text: string }) {
  const lines = text.split("\n");
  if (lines.length > 1 && lines[lines.length - 1] === "") lines.pop();
  return (
    <div className="font-mono text-[12px] leading-[1.6] py-2" dir="ltr">
      {lines.map((l, i) => (
        <div key={i} className="flex hover:bg-panel-2">
          <span className="w-12 shrink-0 pe-3 text-end text-faint select-none tabular-nums">{i + 1}</span>
          <span className="whitespace-pre-wrap break-all pe-4">{l || " "}</span>
        </div>
      ))}
    </div>
  );
}
