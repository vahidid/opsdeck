import { ReactFlowProvider } from "@xyflow/react";
import { Editor } from "./editor";

export function TopologyPage() {
  return (
    <ReactFlowProvider>
      <Editor />
    </ReactFlowProvider>
  );
}
