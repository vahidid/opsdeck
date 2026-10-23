import { useEffect, useRef } from "react";
import { EditorState, Compartment } from "@codemirror/state";
import type { Extension } from "@codemirror/state";
import {
  EditorView,
  crosshairCursor,
  drawSelection,
  dropCursor,
  highlightActiveLine,
  highlightActiveLineGutter,
  highlightSpecialChars,
  keymap,
  lineNumbers,
  rectangularSelection,
} from "@codemirror/view";
import { defaultKeymap, history, historyKeymap, indentWithTab } from "@codemirror/commands";
import { HighlightStyle, StreamLanguage, bracketMatching, foldGutter, foldKeymap, indentOnInput, indentUnit, syntaxHighlighting } from "@codemirror/language";
import { yaml as yamlLang } from "@codemirror/lang-yaml";
import { jinja2 } from "@codemirror/legacy-modes/mode/jinja2";
import { properties } from "@codemirror/legacy-modes/mode/properties";
import { shell } from "@codemirror/legacy-modes/mode/shell";
import { python } from "@codemirror/legacy-modes/mode/python";
import { javascript, json } from "@codemirror/legacy-modes/mode/javascript";
import { mySQL, pgSQL } from "@codemirror/legacy-modes/mode/sql";
import { highlightSelectionMatches, search, searchKeymap } from "@codemirror/search";
import { autocompletion, closeBrackets, closeBracketsKeymap, completionKeymap } from "@codemirror/autocomplete";
import type { CompletionContext, CompletionResult } from "@codemirror/autocomplete";
import type { StringStream } from "@codemirror/language";
import { linter, lintGutter, lintKeymap } from "@codemirror/lint";
import type { Diagnostic } from "@codemirror/lint";
import { tags as t } from "@lezer/highlight";
import { COMPLETE_TASK, PLAY_KEYWORDS, parseYaml, yamlProblems } from "./model";

export type Language = "yaml" | "jinja" | "ini" | "shell" | "json" | "python" | "pgsql" | "mysql" | "mongo" | "redis" | "text";
export type ModuleItem = { name: string; description: string };

const theme = EditorView.theme({
  "&": { height: "100%", fontSize: "12.5px", backgroundColor: "transparent", color: "var(--c-text)" },
  ".cm-scroller": { fontFamily: "var(--font-mono)", lineHeight: "1.6" },
  ".cm-content": { caretColor: "var(--c-accent)", paddingBlock: "8px" },
  ".cm-gutters": { backgroundColor: "var(--c-panel)", color: "var(--c-faint)", border: "none", borderRight: "1px solid var(--c-border)" },
  ".cm-activeLine": { backgroundColor: "color-mix(in srgb, var(--c-panel-3) 55%, transparent)" },
  ".cm-activeLineGutter": { backgroundColor: "var(--c-panel-2)", color: "var(--c-muted)" },
  "&.cm-focused .cm-cursor": { borderLeftColor: "var(--c-accent)" },
  "&.cm-focused .cm-selectionBackground, .cm-selectionBackground, ::selection": { backgroundColor: "color-mix(in srgb, var(--c-accent) 28%, transparent) !important" },
  ".cm-selectionMatch": { backgroundColor: "color-mix(in srgb, var(--c-accent) 14%, transparent)" },
  ".cm-matchingBracket": { backgroundColor: "color-mix(in srgb, var(--c-accent) 25%, transparent)", outline: "none" },
  ".cm-foldPlaceholder": { backgroundColor: "var(--c-panel-3)", border: "1px solid var(--c-border)", color: "var(--c-muted)" },
  ".cm-tooltip": { backgroundColor: "var(--c-panel-2)", border: "1px solid var(--c-border-strong)", color: "var(--c-text)" },
  ".cm-tooltip-autocomplete > ul > li[aria-selected]": { backgroundColor: "var(--c-panel-3)", color: "var(--c-text)" },
  ".cm-completionDetail": { color: "var(--c-faint)", fontStyle: "normal", marginLeft: "1em" },
  ".cm-panels": { backgroundColor: "var(--c-panel-2)", color: "var(--c-text)", borderTop: "1px solid var(--c-border)" },
  ".cm-panel.cm-search input, .cm-panel.cm-search button": { fontSize: "12px" },
  ".cm-textfield": { backgroundColor: "var(--c-panel)", border: "1px solid var(--c-border-strong)", color: "var(--c-text)", borderRadius: "4px" },
  ".cm-button": { backgroundImage: "none", backgroundColor: "var(--c-panel-3)", border: "1px solid var(--c-border-strong)", color: "var(--c-text)", borderRadius: "4px" },
  ".cm-diagnostic-error": { borderLeftColor: "var(--c-err)" },
  ".cm-diagnostic-warning": { borderLeftColor: "var(--c-warn)" },
});

const highlight = HighlightStyle.define([
  { tag: [t.propertyName, t.definition(t.propertyName)], color: "#60a5fa" },
  { tag: [t.string, t.special(t.string)], color: "#a3e635" },
  { tag: [t.number, t.bool, t.null, t.atom], color: "#f59e0b" },
  { tag: [t.comment, t.lineComment, t.blockComment], color: "var(--c-faint)", fontStyle: "italic" },
  { tag: [t.keyword, t.controlKeyword, t.operatorKeyword], color: "#c084fc" },
  { tag: [t.typeName, t.tagName, t.labelName], color: "#22d3ee" },
  { tag: [t.variableName, t.name], color: "#f472b6" },
  { tag: [t.meta, t.processingInstruction, t.punctuation, t.separator], color: "var(--c-muted)" },
  { tag: t.heading, color: "#60a5fa", fontWeight: "600" },
  { tag: t.invalid, color: "var(--c-err)" },
]);

function languageExt(lang: Language): Extension {
  switch (lang) {
    case "yaml":
      return yamlLang();
    case "jinja":
      return StreamLanguage.define(jinja2);
    case "ini":
      return StreamLanguage.define(properties);
    case "shell":
      return StreamLanguage.define(shell);
    case "python":
      return StreamLanguage.define(python);
    case "json":
      return StreamLanguage.define(json);
    case "pgsql":
      return StreamLanguage.define(pgSQL);
    case "mysql":
      return StreamLanguage.define(mySQL);
    case "mongo":
      return StreamLanguage.define(javascript);
    case "redis":
      return StreamLanguage.define(redisMode);
  }
  return [];
}

// redis-cli lines: the command, then quoted or plain arguments.
const redisMode = {
  name: "redis",
  startState: () => ({ first: true }),
  token(stream: StringStream, state: { first: boolean }) {
    if (stream.sol()) state.first = true;
    if (stream.eatSpace()) return null;
    if (state.first && (stream.match("#") || stream.match("//"))) {
      stream.skipToEnd();
      return "comment";
    }
    const q = stream.peek();
    if (q === '"' || q === "'") {
      stream.next();
      let esc = false;
      let c: string | void;
      while ((c = stream.next()) != null) {
        if (c === q && !esc) break;
        esc = !esc && c === "\\";
      }
      state.first = false;
      return "string";
    }
    stream.match(/^[^\s"']+/);
    if (state.first) {
      state.first = false;
      return "keyword";
    }
    return /^-?\d+(\.\d+)?$/.test(stream.current()) ? "number" : null;
  },
};

function yamlLint(view: EditorView): Diagnostic[] {
  const text = view.state.doc.toString();
  if (!text.trim()) return [];
  const doc = parseYaml(text);
  return yamlProblems(doc).map((p) => {
    const from = Math.min(p.from, text.length);
    return { from, to: Math.min(Math.max(p.to, from), text.length), severity: p.error ? "error" : "warning", message: p.message };
  });
}

// Ansible-aware completion in YAML: at a key position, task and play
// keywords and module names (from ansible-doc).
function ansibleCompletion(modules: () => ModuleItem[]) {
  return (ctx: CompletionContext): CompletionResult | null => {
    const line = ctx.state.doc.lineAt(ctx.pos);
    const before = line.text.slice(0, ctx.pos - line.from);
    const m = /^(\s*(?:-\s+)?)([\w.]*)$/.exec(before);
    if (!m || (!ctx.explicit && m[2].length < 2)) return null;
    const from = ctx.pos - m[2].length;
    const kw = new Set([...COMPLETE_TASK, ...PLAY_KEYWORDS]);
    const options = [
      ...Array.from(kw).map((k) => ({ label: k, type: "keyword", apply: k + ": ", boost: 2 })),
      ...modules().map((mod) => ({ label: mod.name, type: "function", detail: mod.description.slice(0, 60), apply: mod.name + ":\n" + " ".repeat(m[1].replace("-", " ").length + 2) })),
    ];
    return { from, options, validFor: /^[\w.]*$/ };
  };
}

// Query completion: the language's keywords (SQL, MongoDB methods, Redis
// commands) and the database's names (tables, collections, keys).
const SQL_KEYWORDS = ["SELECT", "FROM", "WHERE", "GROUP BY", "ORDER BY", "LIMIT", "JOIN", "LEFT JOIN", "INSERT INTO", "VALUES", "UPDATE", "SET", "DELETE FROM", "WITH", "AS", "AND", "OR", "NOT", "NULL", "IS", "IN", "LIKE", "COUNT", "DISTINCT", "HAVING", "EXPLAIN", "SHOW"];
const MONGO_KEYWORDS = ["find", "findOne", "aggregate", "countDocuments", "estimatedDocumentCount", "distinct", "getIndexes", "sort", "limit", "skip", "projection",
  "insertOne", "insertMany", "updateOne", "updateMany", "replaceOne", "deleteOne", "deleteMany", "findOneAndUpdate", "createIndex", "dropIndex", "runCommand",
  "getCollection", "getCollectionNames", "stats", "show collections", "show dbs", "use", "ObjectId", "ISODate", "NumberLong", "NumberDecimal",
  "$match", "$group", "$project", "$sort", "$limit", "$lookup", "$unwind", "$count", "$sum", "$avg", "$in", "$gt", "$gte", "$lt", "$lte", "$ne", "$exists", "$regex", "$set", "$inc", "$push"];
const REDIS_KEYWORDS = ["GET", "MGET", "SET", "DEL", "EXISTS", "TYPE", "TTL", "EXPIRE", "SCAN", "KEYS", "HGET", "HGETALL", "HSET", "HDEL", "HSCAN", "LRANGE", "LLEN", "LPUSH", "RPUSH",
  "SMEMBERS", "SSCAN", "SADD", "SREM", "ZRANGE", "ZSCORE", "ZADD", "ZREM", "XRANGE", "XREVRANGE", "XLEN", "XINFO", "INFO", "DBSIZE", "CONFIG GET", "CLIENT LIST",
  "SLOWLOG GET", "MEMORY USAGE", "OBJECT ENCODING", "MATCH", "COUNT", "WITHSCORES"];
const KEYWORDS: Partial<Record<Language, string[]>> = { pgsql: SQL_KEYWORDS, mysql: SQL_KEYWORDS, mongo: MONGO_KEYWORDS, redis: REDIS_KEYWORDS };
function queryCompletion(language: Language, words: () => string[]) {
  const kws = KEYWORDS[language] ?? [];
  const valid = language === "redis" ? /^[^\s"']*$/ : /^[\w.$]*$/;
  return (ctx: CompletionContext): CompletionResult | null => {
    const w = ctx.matchBefore(language === "redis" ? /[^\s"']+/ : /[\w.$]+/);
    if (!w || (w.from === w.to && !ctx.explicit)) return null;
    return {
      from: w.from,
      options: [...kws.map((k) => ({ label: k, type: "keyword" })), ...words().map((n) => ({ label: n, type: n.includes(".") ? "class" : "property", boost: 1 }))],
      validFor: valid,
    };
  };
}

/**
 * CodeEditor is the text editor of the Ansible page: syntax highlighting,
 * line numbers, folding, search and replace (Ctrl/⌘-F), YAML checks,
 * Ansible completion (Ctrl-Space) and Ctrl/⌘-S to save.
 */
export default function CodeEditor({
  value,
  onChange,
  language,
  onSave,
  readOnly = false,
  modules = [],
  autoFocus = false,
  onRun,
  words,
  wrap = false,
}: {
  value: string;
  onChange?: (v: string) => void;
  language: Language;
  onSave?: () => void;
  readOnly?: boolean;
  modules?: ModuleItem[];
  autoFocus?: boolean;
  /** Ctrl/⌘-Enter (the SQL editor runs the query) */
  onRun?: () => void;
  /** names offered by query completion (tables, collections, keys) */
  words?: string[];
  /** wrap long lines */
  wrap?: boolean;
}) {
  const host = useRef<HTMLDivElement>(null);
  const view = useRef<EditorView | null>(null);
  const cb = useRef({ onChange, onSave, onRun });
  cb.current = { onChange, onSave, onRun };
  const names = useRef(words ?? []);
  names.current = words ?? [];
  const mods = useRef(modules);
  mods.current = modules;
  const ro = useRef(new Compartment());

  useEffect(() => {
    if (!host.current) return;
    const ext: Extension[] = [
      lineNumbers(),
      highlightActiveLineGutter(),
      highlightSpecialChars(),
      history(),
      foldGutter(),
      drawSelection(),
      dropCursor(),
      EditorState.allowMultipleSelections.of(true),
      indentOnInput(),
      indentUnit.of("  "),
      EditorState.tabSize.of(2),
      bracketMatching(),
      closeBrackets(),
      rectangularSelection(),
      crosshairCursor(),
      highlightActiveLine(),
      highlightSelectionMatches(),
      search({ top: true }),
      syntaxHighlighting(highlight),
      theme,
      languageExt(language),
      wrap ? EditorView.lineWrapping : [],
      ro.current.of([EditorState.readOnly.of(readOnly), EditorView.editable.of(!readOnly)]),
      keymap.of([
        { key: "Mod-s", run: () => (cb.current.onSave?.(), true), preventDefault: true },
        { key: "Mod-Enter", run: () => (cb.current.onRun ? (cb.current.onRun(), true) : false), preventDefault: true },
        ...closeBracketsKeymap,
        ...defaultKeymap,
        ...searchKeymap,
        ...historyKeymap,
        ...foldKeymap,
        ...completionKeymap,
        ...lintKeymap,
        indentWithTab,
      ]),
      EditorView.updateListener.of((u) => {
        if (u.docChanged) cb.current.onChange?.(u.state.doc.toString());
      }),
    ];
    if (KEYWORDS[language]) {
      ext.push(autocompletion({ override: [queryCompletion(language, () => names.current)], activateOnTyping: true }));
    }
    if (language === "yaml") {
      ext.push(linter(yamlLint, { delay: 400 }), lintGutter(), autocompletion({ override: [ansibleCompletion(() => mods.current)], activateOnTyping: true }));
    }
    const v = new EditorView({ parent: host.current, state: EditorState.create({ doc: value, extensions: ext }) });
    view.current = v;
    if (autoFocus) v.focus();
    return () => {
      v.destroy();
      view.current = null;
    };
    // the editor is created once per language; value changes are applied below
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [language, wrap]);

  // outside changes (the visual editor, a reload) replace the text
  useEffect(() => {
    const v = view.current;
    if (v && v.state.doc.toString() !== value) {
      v.dispatch({ changes: { from: 0, to: v.state.doc.length, insert: value } });
    }
  }, [value]);

  useEffect(() => {
    view.current?.dispatch({ effects: ro.current.reconfigure([EditorState.readOnly.of(readOnly), EditorView.editable.of(!readOnly)]) });
  }, [readOnly]);

  return <div ref={host} className="h-full min-h-0 overflow-hidden" dir="ltr" />;
}
