// ANSI escape sequences (SGR colours and styles) → styled segments, for
// rendering container logs the way a terminal would. Other escape sequences
// (cursor moves, OSC titles…) are dropped.

export type AnsiStyle = { fg?: string; bg?: string; bold?: boolean; dim?: boolean; italic?: boolean; underline?: boolean };
export type AnsiSegment = { text: string; style: AnsiStyle | null };

// Readable on both the dark and the light theme.
const BASIC = ["#7d8590", "#f87171", "#34d399", "#fbbf24", "#60a5fa", "#c084fc", "#22d3ee", "#c9d1d9"];
const BRIGHT = ["#9aa4b2", "#fca5a5", "#6ee7b7", "#fcd34d", "#93c5fd", "#d8b4fe", "#67e8f9", "#f0f3f6"];

function color256(n: number): string | undefined {
  if (n < 8) return BASIC[n];
  if (n < 16) return BRIGHT[n - 8];
  if (n < 232) {
    const v = n - 16;
    const c = (x: number) => (x === 0 ? 0 : 55 + x * 40);
    return `rgb(${c(Math.floor(v / 36))},${c(Math.floor(v / 6) % 6)},${c(v % 6)})`;
  }
  if (n < 256) {
    const g = 8 + (n - 232) * 10;
    return `rgb(${g},${g},${g})`;
  }
  return undefined;
}

// eslint-disable-next-line no-control-regex
const ESC_RE = /\x1b\[([0-9;:]*)([A-Za-z])|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b[()][A-Za-z0-9]|\x1b[=>78]/g;

export function hasAnsi(s: string): boolean {
  return s.includes("\x1b");
}

export function stripAnsi(s: string): string {
  return hasAnsi(s) ? s.replace(ESC_RE, "") : s;
}

export function parseAnsi(s: string): AnsiSegment[] {
  if (!hasAnsi(s)) return [{ text: s, style: null }];
  const out: AnsiSegment[] = [];
  let style: AnsiStyle = {};
  let last = 0;
  const push = (text: string) => {
    if (!text) return;
    const empty = !style.fg && !style.bg && !style.bold && !style.dim && !style.italic && !style.underline;
    out.push({ text, style: empty ? null : { ...style } });
  };
  ESC_RE.lastIndex = 0;
  let m: RegExpExecArray | null;
  while ((m = ESC_RE.exec(s))) {
    push(s.slice(last, m.index));
    last = m.index + m[0].length;
    if (m[2] !== "m") continue; // only SGR changes the style
    const codes = (m[1] || "0").split(/[;:]/).map((x) => (x === "" ? 0 : Number(x)));
    for (let i = 0; i < codes.length; i++) {
      const c = codes[i];
      if (c === 0) style = {};
      else if (c === 1) style.bold = true;
      else if (c === 2) style.dim = true;
      else if (c === 3) style.italic = true;
      else if (c === 4) style.underline = true;
      else if (c === 22) style.bold = style.dim = false;
      else if (c === 23) style.italic = false;
      else if (c === 24) style.underline = false;
      else if (c >= 30 && c <= 37) style.fg = BASIC[c - 30];
      else if (c >= 90 && c <= 97) style.fg = BRIGHT[c - 90];
      else if (c === 39) style.fg = undefined;
      else if (c >= 40 && c <= 47) style.bg = BASIC[c - 40] + "33";
      else if (c >= 100 && c <= 107) style.bg = BRIGHT[c - 100] + "33";
      else if (c === 49) style.bg = undefined;
      else if (c === 38 || c === 48) {
        let col: string | undefined;
        if (codes[i + 1] === 5) {
          col = color256(codes[i + 2]);
          i += 2;
        } else if (codes[i + 1] === 2) {
          col = `rgb(${codes[i + 2] ?? 0},${codes[i + 3] ?? 0},${codes[i + 4] ?? 0})`;
          i += 4;
        }
        if (c === 38) style.fg = col;
        else style.bg = col;
      }
    }
  }
  push(s.slice(last));
  return out;
}

export function ansiCss(st: AnsiStyle): React.CSSProperties {
  return {
    color: st.fg,
    backgroundColor: st.bg,
    fontWeight: st.bold ? 600 : undefined,
    opacity: st.dim ? 0.6 : undefined,
    fontStyle: st.italic ? "italic" : undefined,
    textDecoration: st.underline ? "underline" : undefined,
  };
}
