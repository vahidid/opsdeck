import { Suspense, lazy } from "react";
import type { ComponentProps } from "react";
import { Loading } from "../../../components/ui";

// CodeMirror is loaded only when an editor is shown.
const CodeEditor = lazy(() => import("./CodeEditor"));

export function Code(props: ComponentProps<typeof CodeEditor>) {
  return (
    <Suspense fallback={<Loading label="Loading the editor…" />}>
      <CodeEditor {...props} />
    </Suspense>
  );
}
