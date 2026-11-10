import { createContext, memo, useContext, useMemo } from "react";
import ReactMarkdown from "react-markdown";
import type { Components } from "react-markdown";
import remarkGfm from "remark-gfm";
import { cx } from "./ui";
import { Code, DataTable, InlineCode, PendingBlock, RICH_LANGS, RichBlock } from "./rich";

type Dir = "auto" | "rtl" | "ltr";

// open: while the text is still streaming, its length — a rich block that
// reaches the end is not complete yet. -1 otherwise.
const Ctx = createContext({ rtl: false, open: -1 });

// eslint-disable-next-line @typescript-eslint/no-explicit-any
function Pre({ node, children, ...props }: any) {
  const { rtl, open } = useContext(Ctx);
  const code = node?.children?.[0];
  const lang = /language-([\w-]+)/.exec(String(code?.properties?.className ?? ""))?.[1] ?? "";
  const src: string = code?.children?.[0]?.value ?? "";
  if (RICH_LANGS.includes(lang)) {
    if (open >= 0 && (node?.position?.end?.offset ?? 0) >= open) return <PendingBlock lang={lang} />;
    return <RichBlock lang={lang} src={src} rtl={rtl} />;
  }
  return (
    <Code text={src.replace(/\n$/, "")} lang={lang} {...props}>
      {children}
    </Code>
  );
}

// The components are built once per direction: a component created during
// render would be a new type every time, remounting the whole message (and
// resetting sorted tables and widgets) on every streamed token.
function components(d: "auto" | "rtl" | undefined): Components {
  const block =
    <T extends keyof JSX.IntrinsicElements>(Tag: T) =>
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    ({ node: _node, ...props }: any) => {
      const C = Tag as unknown as React.ElementType;
      return <C dir={d} {...props} />;
    };
  return {
    a: ({ node: _node, ...props }) => <a {...props} target="_blank" rel="noreferrer" />,
    p: block("p"),
    li: block("li"),
    ul: block("ul"),
    ol: block("ol"),
    h1: block("h1"),
    h2: block("h2"),
    h3: block("h3"),
    h4: block("h4"),
    h5: block("h5"),
    h6: block("h6"),
    blockquote: block("blockquote"),
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    table: (props: any) => <DataTable dir={d} {...props} />,
    th: block("th"),
    td: block("td"),
    pre: Pre,
    code: ({ node: _node, ...props }) => <InlineCode {...props} />,
  };
}
const COMPONENTS: Record<Dir, Components> = { auto: components("auto"), rtl: components("rtl"), ltr: components(undefined) };

// > [!NOTE] … (also TIP, IMPORTANT, WARNING, CAUTION) becomes a callout.
// eslint-disable-next-line @typescript-eslint/no-explicit-any
function callouts(n: any) {
  for (const c of n.children ?? []) callouts(c);
  const first = n.type === "blockquote" && n.children[0]?.type === "paragraph" ? n.children[0].children[0] : null;
  const m = first?.type === "text" ? /^\[!(NOTE|TIP|IMPORTANT|WARNING|CAUTION)\][ \t]*\n?/i.exec(first.value) : null;
  if (!m) return;
  first.value = first.value.slice(m[0].length);
  n.data = { ...n.data, hProperties: { className: "callout callout-" + m[1].toLowerCase() } };
}
const remarkCallouts = () => callouts;
const PLUGINS = [remarkGfm, remarkCallouts];

// Markdown renders prose with bidirectional text in mind: dir="rtl" lays a
// whole message out right-to-left (Persian answers); "auto" lets every
// paragraph, list and table pick its own direction from its first letters.
// Code always stays left-to-right. Fenced chart, stats, timeline and widget
// blocks render as components (see rich.tsx); streaming marks a text that is
// still being written.
export const Markdown = memo(function Markdown({ children, className, dir = "auto", streaming }: { children: string; className?: string; dir?: Dir; streaming?: boolean }) {
  const ctx = useMemo(() => ({ rtl: dir === "rtl", open: streaming ? children.trimEnd().length : -1 }), [dir, streaming, children]);
  return (
    <div className={cx("prose-ops", dir === "rtl" && "prose-rtl", className)} dir={dir === "rtl" ? "rtl" : undefined}>
      <Ctx.Provider value={ctx}>
        <ReactMarkdown remarkPlugins={PLUGINS} components={COMPONENTS[dir]}>
          {children}
        </ReactMarkdown>
      </Ctx.Provider>
    </div>
  );
});
