import { createContext, useContext } from "react";
import type { Document } from "yaml";
import type { AnsibleModuleDoc } from "../../../api/types";
import type { ModuleItem } from "./CodeEditor";

// EditorCtx is what the visual views of one open file work with.
export type EditorCtx = {
  doc: Document;
  /** change the document; the file's text follows */
  edit: (fn: (doc: Document) => void) => void;
  modules: ModuleItem[];
  moduleDoc: (name: string) => Promise<AnsibleModuleDoc | null>;
  /** roles of the project (roles/<name>/) */
  roles: string[];
  /** every file of the project */
  files: string[];
  openFile: (path: string) => void;
  path: string;
  readOnly: boolean;
  /** add or change an inline !vault value (key null: a new one) — plain YAML files only */
  setSecret?: (key: string | null) => void;
};

export const EditorContext = createContext<EditorCtx | null>(null);

export function useEditor(): EditorCtx {
  const c = useContext(EditorContext);
  if (!c) throw new Error("EditorContext missing");
  return c;
}
