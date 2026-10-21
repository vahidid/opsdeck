import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import { cx } from "./ui";

export function Markdown({ children, className }: { children: string; className?: string }) {
  return (
    <div className={cx("prose-ops", className)}>
      <ReactMarkdown
        remarkPlugins={[remarkGfm]}
        components={{
          a: ({ node: _node, ...props }) => <a {...props} target="_blank" rel="noreferrer" />,
        }}
      >
        {children}
      </ReactMarkdown>
    </div>
  );
}
