import type { ReactNode } from "react";
import { ArrowDown, ArrowUp, Bot, Eye, EyeOff, X } from "lucide-react";
import { ACCENTS, useAppearance } from "../context/appearance";
import type { Accent, Corners, Density, MenuMode, Scale, ThemePref } from "../context/appearance";
import { NAV, NAV_GROUPS } from "../lib/nav";
import { Drawer } from "./overlay";
import { Button, IconButton, Segmented, Select, cx } from "./ui";

// CustomizeDrawer is the viewer's personalisation: look, density and which
// sections the menu shows (pinned first, hidden ones only in ⌘K).
export function CustomizeDrawer() {
  const { prefs, set, reset, customizeOpen, setCustomizeOpen } = useAppearance();
  if (!customizeOpen) return null;
  const close = () => setCustomizeOpen(false);
  const hidden = new Set(prefs.hidden);
  const movePin = (i: number, d: -1 | 1) => {
    const p = [...prefs.pinned];
    const j = i + d;
    if (j < 0 || j >= p.length) return;
    [p[i], p[j]] = [p[j], p[i]];
    set({ pinned: p });
  };
  const unpinned = NAV.filter((n) => !prefs.pinned.includes(n.to));

  return (
    <Drawer
      open
      onClose={close}
      width={420}
      title="Customize"
      subtitle="Saved in this browser only"
      footer={
        <>
          <Button variant="ghost" onClick={reset} className="mr-auto">
            Reset to defaults
          </Button>
          <Button variant="primary" onClick={close}>
            Done
          </Button>
        </>
      }
    >
      <div className="space-y-7">
        <section className="space-y-1">
          <SectionTitle>Appearance</SectionTitle>
          <Row label="Theme">
            <Segmented<ThemePref>
              value={prefs.theme}
              onChange={(theme) => set({ theme })}
              options={[
                { id: "dark", label: "Dark" },
                { id: "light", label: "Light" },
                { id: "system", label: "System" },
              ]}
            />
          </Row>
          <Row label="Accent">
            <div role="group" aria-label="Accent colour" className="flex items-center gap-2">
              {ACCENTS.map((a) => (
                <button
                  key={a.id}
                  onClick={() => set({ accent: a.id as Accent })}
                  title={a.label}
                  aria-label={a.label}
                  aria-pressed={prefs.accent === a.id}
                  className={cx(
                    "size-6 rounded-full ring-1 ring-inset ring-white/15 transition-transform",
                    prefs.accent === a.id && "ring-2 ring-offset-2 ring-offset-panel ring-fg scale-105",
                  )}
                  style={{ background: a.swatch || "conic-gradient(#10b981, #3b82f6, #f59e0b, #fb7185, #10b981)" }}
                />
              ))}
            </div>
          </Row>
          <p className="flex items-center gap-1.5 text-[11.5px] text-faint pb-1">
            <Bot className="size-3.5 text-agent-ink" /> Violet always marks the agent, so it is not offered as an accent.
          </p>
          <Row label="Density">
            <Segmented<Density>
              value={prefs.density}
              onChange={(density) => set({ density })}
              options={[
                { id: "comfortable", label: "Comfortable" },
                { id: "compact", label: "Compact" },
              ]}
            />
          </Row>
          <Row label="Interface size">
            <Segmented<Scale>
              value={prefs.scale}
              onChange={(scale) => set({ scale })}
              options={[
                { id: "s", label: "Small" },
                { id: "m", label: "Default" },
                { id: "l", label: "Large" },
              ]}
            />
          </Row>
          <Row label="Corners">
            <Segmented<Corners>
              value={prefs.corners}
              onChange={(corners) => set({ corners })}
              options={[
                { id: "sharp", label: "Sharp" },
                { id: "soft", label: "Soft" },
                { id: "round", label: "Round" },
              ]}
            />
          </Row>
        </section>

        <section className="space-y-2">
          <SectionTitle>Menu</SectionTitle>
          <Row label="Layout">
            <Segmented<MenuMode>
              value={prefs.menu}
              onChange={(menu) => set({ menu })}
              options={[
                { id: "panel", label: "Icons + panel" },
                { id: "rail", label: "Icons only" },
              ]}
            />
          </Row>
          <div className="pt-2">
            <div className="flex items-baseline justify-between mb-1.5">
              <span className="text-[13px]">Pinned</span>
              <span className="text-[11.5px] text-faint">always at the top of the menu</span>
            </div>
            <div className="space-y-1">
              {prefs.pinned.length === 0 && <div className="text-xs text-faint py-1">Nothing pinned. Hover a section in the menu and click the pin.</div>}
              {prefs.pinned.map((id, i) => {
                const n = NAV.find((x) => x.to === id);
                if (!n) return null;
                return (
                  <div key={id} className="flex items-center gap-2 h-9 pl-2.5 pr-1 rounded-md border border-line bg-bg [&>svg]:size-4 [&>svg]:text-faint">
                    {n.icon}
                    <span className="flex-1 text-[13px]">{n.label}</span>
                    <IconButton title="Move up" onClick={() => movePin(i, -1)} disabled={i === 0}>
                      <ArrowUp className="size-3.5" />
                    </IconButton>
                    <IconButton title="Move down" onClick={() => movePin(i, 1)} disabled={i === prefs.pinned.length - 1}>
                      <ArrowDown className="size-3.5" />
                    </IconButton>
                    <IconButton title={`Unpin ${n.label}`} onClick={() => set({ pinned: prefs.pinned.filter((x) => x !== id) })}>
                      <X className="size-3.5" />
                    </IconButton>
                  </div>
                );
              })}
              {unpinned.length > 0 && (
                <Select
                  value=""
                  onChange={(e) => e.target.value && set({ pinned: [...prefs.pinned, e.target.value] })}
                  aria-label="Pin a section"
                  className="text-muted"
                >
                  <option value="">+ Pin a section…</option>
                  {unpinned.map((n) => (
                    <option key={n.to} value={n.to}>
                      {n.label}
                    </option>
                  ))}
                </Select>
              )}
            </div>
          </div>
          <div className="pt-3">
            <div className="text-[13px] mb-0.5">Sections in the menu</div>
            <p className="text-[11.5px] text-faint mb-2">Hide what you don't use. Hidden sections stay reachable from search (Ctrl/⌘+K).</p>
            <div className="space-y-3">
              {NAV_GROUPS.map((g) => (
                <div key={g.id}>
                  <div className="text-[10.5px] font-semibold uppercase tracking-wider text-faint mb-1.5">{g.label}</div>
                  <div className="flex flex-wrap gap-1.5">
                    {NAV.filter((n) => n.group === g.id).map((n) => {
                      const on = !hidden.has(n.to);
                      return (
                        <button
                          key={n.to}
                          aria-pressed={on}
                          onClick={() => set({ hidden: on ? [...prefs.hidden, n.to] : prefs.hidden.filter((x) => x !== n.to) })}
                          className={cx(
                            "inline-flex items-center gap-1.5 h-7 px-2.5 rounded-full border text-[12px] transition-colors [&_svg]:size-3.5",
                            on ? "border-line-strong bg-panel-2 text-fg" : "border-line text-faint line-through",
                          )}
                        >
                          {on ? <Eye /> : <EyeOff />}
                          {n.label}
                        </button>
                      );
                    })}
                  </div>
                </div>
              ))}
            </div>
          </div>
        </section>
      </div>
    </Drawer>
  );
}

function SectionTitle({ children }: { children: ReactNode }) {
  return <h3 className="text-[10.5px] font-semibold uppercase tracking-wider text-faint mb-2">{children}</h3>;
}

function Row({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="flex items-center justify-between gap-3 min-h-10 flex-wrap">
      <span className="text-[13px]">{label}</span>
      {children}
    </div>
  );
}
