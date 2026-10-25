import type { DeployHealth } from "../api/types";
import type { Tone } from "../components/ui";

// Tone of a deployment target's health (Deployments page, agent views).
export function healthTone(h: DeployHealth | undefined): Tone {
  switch (h) {
    case "ok":
      return "ok";
    case "warn":
      return "warn";
    case "err":
      return "err";
    case "busy":
      return "info";
  }
  return "neutral";
}

// shortRev cuts a commit hash to 8 characters (other revisions stay).
export const shortRev = (r?: string) => (r && /^[0-9a-f]{40}$/.test(r) ? r.slice(0, 8) : (r ?? ""));
