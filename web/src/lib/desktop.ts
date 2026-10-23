// The desktop app shows this UI in a webview, which opens neither new
// windows nor downloads. It loads the UI with ?desktop=1; from then on web
// links are handed to the server to open in the default browser, and
// downloads are saved by the server in the downloads folder.

const KEY = "opsdeck.desktop";

export const isDesktop: boolean = (() => {
  try {
    if (new URLSearchParams(location.search).get("desktop") === "1") sessionStorage.setItem(KEY, "1");
    return sessionStorage.getItem(KEY) === "1";
  } catch {
    return false;
  }
})();

type Notice = { kind: "success" | "error"; title: string; detail?: string };
let notify: (n: Notice) => void = () => {};

async function check(res: Response) {
  if (res.ok) return;
  let msg = `${res.status} ${res.statusText}`;
  try {
    msg = ((await res.json()) as { error?: string }).error || msg;
  } catch {
    // not JSON
  }
  throw new Error(msg);
}

const errText = (e: unknown) => (e instanceof Error ? e.message : String(e));

/** Opens a web link in the default browser. */
export async function openExternal(url: string) {
  try {
    const res = await fetch("/api/desktop/open", {
      method: "POST",
      credentials: "same-origin",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ url }),
    });
    await check(res);
  } catch (e) {
    notify({ kind: "error", title: "Could not open the link", detail: errText(e) });
  }
}

/** Saves a file: in the downloads folder in the desktop app, as a browser download otherwise. */
export async function saveFile(data: Blob, name: string) {
  if (!isDesktop) {
    const a = document.createElement("a");
    a.href = URL.createObjectURL(data);
    a.download = name;
    a.click();
    setTimeout(() => URL.revokeObjectURL(a.href), 1000);
    return;
  }
  try {
    const res = await fetch(`/api/desktop/save?name=${encodeURIComponent(name)}`, { method: "POST", credentials: "same-origin", body: data });
    await check(res);
    const { path } = (await res.json()) as { path: string };
    notify({ kind: "success", title: "Saved to Downloads", detail: path });
  } catch (e) {
    notify({ kind: "error", title: "Download failed", detail: errText(e) });
  }
}

// filename="…" (or filename*=UTF-8''…) of a Content-Disposition header
function dispositionName(h: string | null): string {
  if (!h) return "";
  const star = /filename\*\s*=\s*UTF-8''([^;]+)/i.exec(h);
  if (star) {
    try {
      return decodeURIComponent(star[1].trim());
    } catch {
      // fall through
    }
  }
  const m = /filename\s*=\s*"?([^";]+)"?/i.exec(h);
  return m ? m[1].trim() : "";
}

async function download(url: URL, suggested: string | null) {
  try {
    const res = await fetch(url, { credentials: "same-origin" });
    await check(res);
    const name = suggested || dispositionName(res.headers.get("Content-Disposition")) || decodeURIComponent(url.pathname.split("/").pop() || "download");
    await saveFile(await res.blob(), name);
  } catch (e) {
    notify({ kind: "error", title: "Download failed", detail: errText(e) });
  }
}

const isWeb = (u: URL) => u.protocol === "http:" || u.protocol === "https:";

function onClick(e: MouseEvent) {
  if (e.defaultPrevented || e.button > 1) return;
  const a = (e.target as Element | null)?.closest?.("a[href]") as HTMLAnchorElement | null;
  if (!a) return;
  let url: URL;
  try {
    url = new URL(a.href, location.href);
  } catch {
    return;
  }
  if (url.origin === location.origin) {
    // links into the API are downloads (exports, logs, volume files)
    if (a.hasAttribute("download") || url.pathname.startsWith("/api/")) {
      e.preventDefault();
      void download(url, a.getAttribute("download"));
    }
    return;
  }
  if (isWeb(url)) {
    e.preventDefault();
    void openExternal(url.href);
  }
}

/** Routes links, window.open and downloads to the desktop; returns the undo. */
export function installDesktop(n: (n: Notice) => void): () => void {
  notify = n;
  const open = window.open;
  window.open = (target?: string | URL, name?: string, features?: string) => {
    if (target !== undefined && target !== "") {
      const u = new URL(String(target), location.href);
      if (u.origin !== location.origin && isWeb(u)) {
        void openExternal(u.href);
        return null;
      }
    }
    return open.call(window, target, name, features);
  };
  document.addEventListener("click", onClick, true);
  document.addEventListener("auxclick", onClick, true);
  return () => {
    window.open = open;
    document.removeEventListener("click", onClick, true);
    document.removeEventListener("auxclick", onClick, true);
    notify = () => {};
  };
}
