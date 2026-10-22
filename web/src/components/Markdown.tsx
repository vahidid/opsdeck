import ReactMarkdown from "react-markdown";
import type { Components } from "react-markdown";
import remarkGfm from "remark-gfm";
import { cx } from "./ui";

// Markdown renders prose with bidirectional text in mind: dir="rtl" lays a
// whole message out right-to-left (Persian answers); "auto" lets every
// paragraph, list and table pick its own direction from its first letters.
// Code always stays left-to-right.
export function Markdown({ children, className, dir = "auto" }: { children: string; className?: string; dir?: "auto" | "rtl" | "ltr" }) {
  const d = dir === "ltr" ? undefined : dir;
  const block =
    <T extends keyof JSX.IntrinsicElements>(Tag: T) =>
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    ({ node: _node, ...props }: any) => {
      const C = Tag as unknown as React.ElementType;
      return <C dir={d} {...props} />;
    };
  const components: Components = {
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
    table: block("table"),
    th: block("th"),
    td: block("td"),
    pre: ({ node: _node, ...props }) => <pre dir="ltr" {...props} />,
    code: ({ node: _node, ...props }) => <code dir="ltr" {...props} />,
  };
  return (
    <div className={cx("prose-ops", dir === "rtl" && "prose-rtl", className)} dir={dir === "rtl" ? "rtl" : undefined}>
      <ReactMarkdown remarkPlugins={[remarkGfm]} components={components}>
        {children}
      </ReactMarkdown>
    </div>
  );
}
