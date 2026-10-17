import React from "react";
import { AbsoluteFill, Easing, OffthreadVideo, getInputProps, interpolate, spring, staticFile, useCurrentFrame, useVideoConfig } from "remotion";

// still: the GIF's render (scripts/promo). Ambient motion (the drifting light,
// the floating phone) changes every pixel of every frame, which a GIF can only
// store as a whole new frame; without it the GIF is a third of the size.
const STILL = Boolean(getInputProps().still);
import { C, MONO, SANS } from "./theme";

// Slow light behind everything: three blurred colour fields drifting, a faint
// dot grid, and a vignette.
export const Backdrop: React.FC<{ offset?: number }> = ({ offset = 0 }) => {
  const f = STILL ? 0 : useCurrentFrame() + offset;
  const blob = (color: string, x: number, y: number, r: number, phase: number) => {
    const dx = Math.sin(f / 90 + phase) * 80, dy = Math.cos(f / 110 + phase) * 60;
    return (
      <div style={{ position: "absolute", left: x + dx - r, top: y + dy - r, width: r * 2, height: r * 2, borderRadius: "50%",
        background: color, filter: "blur(120px)", opacity: 0.32 }} />
    );
  };
  return (
    <AbsoluteFill style={{ background: C.bg, overflow: "hidden" }}>
      {blob(C.violet, 300, 260, 360, 0)}
      {blob(C.cyan, 1620, 820, 340, 2)}
      {blob(C.teal, 1100, 120, 240, 4)}
      <AbsoluteFill style={{ backgroundImage: `radial-gradient(${C.line} 1.2px, transparent 1.2px)`, backgroundSize: "28px 28px", opacity: 0.55,
        maskImage: "radial-gradient(ellipse at center, black 30%, transparent 75%)" }} />
      <AbsoluteFill style={{ background: "radial-gradient(ellipse at center, transparent 55%, rgba(0,0,0,.55) 100%)" }} />
    </AbsoluteFill>
  );
};

// pop: a spring from 0 to 1 starting at frame `at`.
export const usePop = (at: number, damping = 14) => {
  const f = useCurrentFrame(), { fps } = useVideoConfig();
  return spring({ frame: f - at, fps, config: { damping, mass: 0.7 } });
};

export const Kicker: React.FC<{ children: React.ReactNode; at?: number }> = ({ children, at = 0 }) => {
  const p = usePop(at);
  return (
    <div style={{ fontFamily: MONO, fontSize: 22, letterSpacing: 6, textTransform: "uppercase", color: C.cyan, opacity: p,
      transform: `translateX(${(1 - p) * -30}px)`, display: "flex", alignItems: "center", gap: 14 }}>
      <span style={{ color: C.purple }}>✦</span>
      {children}
    </div>
  );
};

// Headline: words rise in one after another; words in `accent` get the
// purple-to-cyan gradient.
export const Headline: React.FC<{ text: string; accent?: string[]; at?: number; size?: number; align?: "left" | "center" }> = ({
  text, accent = [], at = 4, size = 72, align = "left",
}) => {
  const f = useCurrentFrame(), { fps } = useVideoConfig();
  const words = text.split(" ");
  return (
    <div style={{ fontFamily: SANS, fontWeight: 800, fontSize: size, lineHeight: 1.06, letterSpacing: -1.5, color: C.bright, textAlign: align }}>
      {words.map((w, i) => {
        const p = spring({ frame: f - at - i * 3, fps, config: { damping: 15, mass: 0.6 } });
        const hot = accent.includes(w.replace(/[.,]/g, ""));
        return (
          <span key={i} style={{ display: "inline-block", marginRight: "0.26em", opacity: p, transform: `translateY(${(1 - p) * 34}px)`,
            ...(hot ? { background: `linear-gradient(90deg, ${C.purple}, ${C.cyan})`, WebkitBackgroundClip: "text", color: "transparent" } : {}) }}>
            {w}
          </span>
        );
      })}
    </div>
  );
};

// Chip: a caption that springs in at `at`, and dims once a later one arrives.
export const Chip: React.FC<{ at: number; until?: number; leave?: boolean; icon?: string; tone?: string; children: React.ReactNode }> = ({
  at, until, leave = false, icon = "→", tone = C.teal, children,
}) => {
  const f = useCurrentFrame();
  const p = usePop(at, 13);
  const dimmed = until !== undefined ? interpolate(f, [until, until + 10], [1, leave ? 0 : 0.45], { extrapolateLeft: "clamp", extrapolateRight: "clamp" }) : 1;
  if (f < at) return null;
  return (
    <div style={{ display: "flex", alignItems: "center", gap: 16, padding: "18px 26px", borderRadius: 18, background: "rgba(26,27,38,.86)",
      border: `1.5px solid ${tone}55`, boxShadow: `0 12px 40px rgba(0,0,0,.35), 0 0 0 1px rgba(255,255,255,.03) inset`,
      fontFamily: SANS, fontSize: 28, fontWeight: 600, color: C.text, opacity: p * dimmed, transform: `translateY(${(1 - p) * 26}px) scale(${0.96 + p * 0.04})`,
      transformOrigin: "left center", maxWidth: 700, ...(leave ? { position: "absolute", top: 0, maxWidth: 1000, fontSize: 26 } : {}) }}>
      <span style={{ color: tone, fontFamily: MONO, fontWeight: 700, fontSize: 28 }}>{icon}</span>
      <span>{children}</span>
    </div>
  );
};

export const Clip: React.FC<{ name: string }> = ({ name }) => (
  <OffthreadVideo src={staticFile(`clips/${name}.mp4`)} muted style={{ width: "100%", height: "100%", display: "block" }} />
);

// A phone: the clip inside a dark bezel, tilted a little and floating.
export const Phone: React.FC<{ name: string; width?: number; aspect: number; tilt?: number }> = ({ name, width = 420, aspect, tilt = -9 }) => {
  const f = useCurrentFrame(), { durationInFrames } = useVideoConfig();
  const ry = STILL ? tilt * 0.6 : interpolate(f, [0, durationInFrames], [tilt, tilt * 0.35], { easing: Easing.inOut(Easing.cubic) });
  const float = STILL ? 0 : Math.sin(f / 40) * 6;
  const enter = usePop(0, 16);
  return (
    <div style={{ perspective: 1800 }}>
      <div style={{ width: width + 28, padding: 14, borderRadius: 64, background: "linear-gradient(145deg, #2a2c3d, #0c0d14 40%, #191a26)",
        boxShadow: `0 40px 120px rgba(0,0,0,.6), 0 0 80px ${C.violet}33, 0 0 0 1.5px #3b3e57 inset`,
        transform: `rotateY(${ry}deg) rotateX(3deg) translateY(${float + (1 - enter) * 80}px)`, opacity: enter }}>
        <div style={{ width, height: width * aspect, borderRadius: 50, overflow: "hidden", background: C.panel }}>
          <Clip name={name} />
        </div>
      </div>
    </div>
  );
};

export type Zoom = { at: number; scale: number; x: number; y: number }; // x, y: the point (0-1) kept in view

// A terminal window around a clip, with keyframed zooms onto what matters.
export const Window: React.FC<{ name: string; width: number; aspect: number; title: string; zooms?: Zoom[] }> = ({ name, width, aspect, title, zooms = [] }) => {
  const f = useCurrentFrame();
  const enter = usePop(0, 16);
  const keys: Zoom[] = [{ at: 0, scale: 1, x: 0.5, y: 0.5 }, ...zooms];
  let z = keys[0];
  for (let i = 0; i < keys.length; i++) {
    const a = keys[i], b = keys[i + 1];
    if (f >= a.at) {
      z = a;
      if (b && f < b.at + 24) {
        const t = interpolate(f, [b.at, b.at + 24], [0, 1], { easing: Easing.inOut(Easing.cubic), extrapolateLeft: "clamp", extrapolateRight: "clamp" });
        z = { at: 0, scale: a.scale + (b.scale - a.scale) * t, x: a.x + (b.x - a.x) * t, y: a.y + (b.y - a.y) * t };
        break;
      }
    }
  }
  const h = width * aspect;
  return (
    <div style={{ width, borderRadius: 18, overflow: "hidden", background: C.panel, opacity: enter, transform: `translateY(${(1 - enter) * 60}px)`,
      boxShadow: `0 40px 120px rgba(0,0,0,.55), 0 0 70px ${C.cyan}22, 0 0 0 1.5px ${C.line}` }}>
      <div style={{ height: 44, display: "flex", alignItems: "center", gap: 10, padding: "0 18px", background: "#16161e", borderBottom: `1px solid ${C.line}` }}>
        {["#ff5f57", "#febc2e", "#28c840"].map((c) => <div key={c} style={{ width: 14, height: 14, borderRadius: 7, background: c }} />)}
        <div style={{ flex: 1, textAlign: "center", fontFamily: MONO, fontSize: 18, color: C.dim, marginRight: 60 }}>{title}</div>
      </div>
      <div style={{ width, height: h, overflow: "hidden" }}>
        <div style={{ width, height: h, transformOrigin: `${z.x * 100}% ${z.y * 100}%`, transform: `scale(${z.scale})` }}>
          <Clip name={name} />
        </div>
      </div>
    </div>
  );
};
