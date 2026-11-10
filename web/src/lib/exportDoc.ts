// A rendered answer or report as one self-contained HTML file: the app's
// styles, the Persian font and the light theme inlined, so it opens anywhere
// and prints to PDF from the browser.
import f400 from "@fontsource/vazirmatn/files/vazirmatn-arabic-400-normal.woff2?inline";
import f700 from "@fontsource/vazirmatn/files/vazirmatn-arabic-700-normal.woff2?inline";
import { saveFile } from "./desktop";

/** Vazirmatn (Arabic script subset) as data URLs, for documents that cannot load the app's fonts */
export const vazirFace = `@font-face{font-family:Vazirmatn;font-weight:400;src:url(${f400}) format("woff2")}@font-face{font-family:Vazirmatn;font-weight:700;src:url(${f700}) format("woff2")}`;

const VARS = [
  ..."bg panel panel-2 panel-3 border border-strong text muted faint accent accent-fg ok warn err info".split(" ").map((n) => "--c-" + n),
  ...[1, 2, 3, 4, 5, 6, 7, 8].map((n) => "--viz-" + n),
];

/** the theme's colours as a :root rule, for widget frames; light=true reads the light theme */
export function themeVars(light?: boolean): string {
  const root = document.documentElement;
  const was = root.className;
  if (light) root.classList.add("light");
  const cs = getComputedStyle(root);
  const out = VARS.map((v) => `${v}:${cs.getPropertyValue(v).trim()}`).join(";");
  const scheme = root.classList.contains("light") ? "light" : "dark";
  root.className = was;
  return `:root{${out};color-scheme:${scheme}}`;
}

/** widgets that have not started yet (they start when scrolled into view): their box, and how to build their document */
export const pendingWidgets = new WeakMap<Element, () => string>();

const esc = (s: string) => s.replace(/[&<>"]/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" })[c]!);

export function exportHtml(el: HTMLElement, title: string) {
  let css = "";
  for (const sheet of Array.from(document.styleSheets)) {
    try {
      for (const r of Array.from(sheet.cssRules)) if (!(r instanceof CSSFontFaceRule)) css += r.cssText + "\n";
    } catch {
      /* a stylesheet of another origin */
    }
  }
  const clone = el.cloneNode(true) as HTMLElement;
  clone.querySelectorAll(".no-export").forEach((n) => n.remove());
  // a widget that never came into view has no frame yet: give its copy one
  const boxes = clone.querySelectorAll(".rb-widget");
  el.querySelectorAll(".rb-widget").forEach((box, i) => {
    const doc = pendingWidgets.get(box);
    if (!doc || box.querySelector("iframe")) return;
    const f = document.createElement("iframe");
    f.setAttribute("sandbox", "allow-scripts");
    f.setAttribute("srcdoc", doc());
    f.style.cssText = "display:block;width:100%;border:0;height:120px";
    boxes[i].appendChild(f);
  });
  // widget frames carry the theme they were rendered with: give them the light one
  const light = themeVars(true);
  clone.querySelectorAll("iframe[srcdoc]").forEach((f) => f.setAttribute("srcdoc", f.getAttribute("srcdoc")!.replace(/:root\{[^}]*\}/, light)));
  const root = document.documentElement;
  const accent = root.dataset.accent ? ` data-accent="${esc(root.dataset.accent)}"` : "";
  const html = `<!doctype html>
<html class="light"${accent} lang="${textLang(el)}">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>${esc(title)}</title>
<style>${vazirFace}
${css}
html,body{height:auto;overflow:auto}
body{padding:32px 20px;font-size:14px}
main{max-width:880px;margin:0 auto}
.rb-code pre{max-height:none!important}
@media print{body{padding:0}figure,.rb-keep{break-inside:avoid}}
</style>
</head>
<body><main>${clone.outerHTML}</main>
<script>addEventListener("message",function(e){var h=e.data&&e.data.opsdeckWidgetHeight;if(!(h>0))return;document.querySelectorAll("iframe").forEach(function(f){if(f.contentWindow===e.source)f.style.height=f.parentElement.style.height=Math.min(900,Math.ceil(h))+"px"})})</script>
</body>
</html>
`;
  const name = title.replace(/\.md$/, "").replace(/[\\/:*?"<>|\s]+/g, "-").replace(/^-+|-+$/g, "") || "report";
  return saveFile(new Blob([html], { type: "text/html" }), name + ".html");
}

function textLang(el: HTMLElement): string {
  return el.querySelector('[dir="rtl"]') || el.getAttribute("dir") === "rtl" ? "fa" : "en";
}
