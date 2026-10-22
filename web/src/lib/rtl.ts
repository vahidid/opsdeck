// Text direction for Persian (and Arabic/Hebrew) content.

const RTL_CHARS = /[֐-ࣿיִ-﷿ﹰ-﻿]/g;
const LTR_CHARS = /[A-Za-zÀ-ɏͰ-ϿЀ-ӿ]/g;

// textDir returns "rtl" when right-to-left script dominates the prose of a
// message. Code, commands and URLs are ignored, so a Persian answer full of
// shell commands still reads right-to-left.
export function textDir(text: string): "rtl" | "ltr" {
  const prose = text
    .replace(/```[\s\S]*?(```|$)/g, " ")
    .replace(/`[^`\n]*`/g, " ")
    .replace(/https?:\/\/\S+/g, " ")
    .replace(/[\w.-]+\/[\w./-]+/g, " "); // paths
  const rtl = prose.match(RTL_CHARS)?.length ?? 0;
  const ltr = prose.match(LTR_CHARS)?.length ?? 0;
  return rtl > 0 && rtl >= 0.4 * (rtl + ltr) ? "rtl" : "ltr";
}
