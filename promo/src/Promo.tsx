import React from "react";
import { AbsoluteFill, interpolate, useCurrentFrame } from "remotion";
import { TransitionSeries, springTiming } from "@remotion/transitions";
import { fade } from "@remotion/transitions/fade";
import { slide } from "@remotion/transitions/slide";
import manifest from "../public/clips/manifest.json";
import "./fonts";
import { C, FPS, MONO, SANS } from "./theme";
import { Backdrop, Chip, Headline, Kicker, Phone, Window, usePop } from "./ui";

type Clip = { duration: number; width: number; height: number; marks: Record<string, number> };
const clips = manifest as unknown as Record<string, Clip>;
const frames = (name: string) => Math.floor(clips[name].duration * FPS);
const mark = (name: string, m: string) => Math.round(clips[name].marks[m] * FPS);
const aspect = (name: string) => clips[name].height / clips[name].width;

const T = 20; // frames each transition overlaps
const INTRO = 120, PIPELINE = 150, OUTRO = 165;

// The scenes, with their lengths: each clip plays in full.
const scenes: [string, number][] = [
  ["intro", INTRO],
  ["approvals", frames("approvals")],
  ["grant", frames("grant")],
  ["terminal", frames("terminal")],
  ["chat", frames("chat")],
  ["audit", frames("audit")],
  ["pipeline", PIPELINE],
  ["outro", OUTRO],
];
export const promoFrames = scenes.reduce((n, [, d]) => n + d, 0) - (scenes.length - 1) * T;

// A phone scene: words on the left, the phone on the right.
const PhoneScene: React.FC<{ clip: string; kicker: string; headline: string; accent: string[]; children?: React.ReactNode }> = ({
  clip, kicker, headline, accent, children,
}) => (
  <AbsoluteFill style={{ flexDirection: "row", alignItems: "center", padding: "0 150px", gap: 90 }}>
    <div style={{ flex: 1, display: "flex", flexDirection: "column", gap: 34 }}>
      <Kicker>{kicker}</Kicker>
      <Headline text={headline} accent={accent} />
      <div style={{ display: "flex", flexDirection: "column", gap: 18, marginTop: 16 }}>{children}</div>
    </div>
    <Phone name={clip} aspect={aspect(clip)} />
  </AbsoluteFill>
);

// A TUI scene: words on top, the terminal below.
const TuiScene: React.FC<{ clip: string; kicker: string; headline: string; accent: string[]; zooms?: Parameters<typeof Window>[0]["zooms"]; children?: React.ReactNode }> = ({
  clip, kicker, headline, accent, zooms, children,
}) => (
  <AbsoluteFill style={{ alignItems: "center", paddingTop: 40, gap: 22 }}>
    <div style={{ width: 1140, display: "flex", flexDirection: "column", gap: 16 }}>
      <Kicker>{kicker}</Kicker>
      <Headline text={headline} accent={accent} size={54} />
    </div>
    <Window name={clip} width={1140} aspect={aspect(clip)} title="supervisor — the TUI, over SSH" zooms={zooms} />
    <div style={{ position: "relative", width: 1140, height: 74, display: "flex", justifyContent: "center" }}>{children}</div>
  </AbsoluteFill>
);

const Intro: React.FC = () => {
  const f = useCurrentFrame();
  const star = usePop(0, 10);
  const glow = 0.6 + Math.sin(f / 8) * 0.15;
  return (
    <AbsoluteFill style={{ alignItems: "center", justifyContent: "center", gap: 34 }}>
      <div style={{ fontSize: 130, color: C.purple, transform: `scale(${star}) rotate(${(1 - star) * -90}deg)`, filter: `drop-shadow(0 0 ${40 * glow}px ${C.violet})` }}>✦</div>
      <div style={{ display: "flex", gap: 6 }}>
        {"eldar-os".split("").map((ch, i) => {
          const p = usePop(8 + i * 3, 12);
          return (
            <span key={i} style={{ fontFamily: MONO, fontWeight: 700, fontSize: 132, letterSpacing: 4, opacity: p, transform: `translateY(${(1 - p) * 40}px)`,
              background: `linear-gradient(180deg, ${C.bright}, ${C.text})`, WebkitBackgroundClip: "text", color: "transparent" }}>{ch}</span>
          );
        })}
      </div>
      <Headline text="The metal under a one-person software factory." accent={["software", "factory."]} at={34} size={52} align="center" />
      <div style={{ fontFamily: SANS, fontSize: 30, color: C.dim, opacity: usePop(58), marginTop: 6 }}>
        Many Claude Code agents. No credentials of their own. You decide.
      </div>
    </AbsoluteFill>
  );
};

const Pipeline: React.FC = () => {
  const steps = [
    { icon: "⎇", label: "Agent opens a PR", tone: C.purple },
    { icon: "✓", label: "You merge it", tone: C.teal },
    { icon: "🔏", label: "Signatures checked", tone: C.cyan },
    { icon: "⚡", label: "Built and live", tone: C.orange },
  ];
  const f = useCurrentFrame();
  const clock = Math.min(15, Math.max(0, Math.floor(interpolate(f, [40, 110], [0, 15], { extrapolateRight: "clamp" }))));
  return (
    <AbsoluteFill style={{ alignItems: "center", justifyContent: "center", gap: 70 }}>
      <div style={{ display: "flex", flexDirection: "column", alignItems: "center", gap: 26 }}>
        <Kicker>It changes itself, too</Kicker>
        <Headline text="Agents propose. You merge. Live in seconds." accent={["Live", "seconds."]} size={64} align="center" />
      </div>
      <div style={{ display: "flex", alignItems: "center", gap: 26 }}>
        {steps.map((s, i) => {
          const p = usePop(20 + i * 16, 13);
          return (
            <React.Fragment key={i}>
              {i > 0 && <div style={{ width: 70, height: 3, background: `linear-gradient(90deg, ${steps[i - 1].tone}, ${s.tone})`, opacity: p, transform: `scaleX(${p})`, transformOrigin: "left" }} />}
              <div style={{ width: 250, padding: "30px 20px", borderRadius: 22, background: "rgba(26,27,38,.9)", border: `1.5px solid ${s.tone}66`,
                display: "flex", flexDirection: "column", alignItems: "center", gap: 16, opacity: p, transform: `translateY(${(1 - p) * 30}px)`,
                boxShadow: `0 0 50px ${s.tone}22` }}>
                <div style={{ fontSize: 50 }}>{s.icon}</div>
                <div style={{ fontFamily: SANS, fontWeight: 700, fontSize: 26, color: C.bright, textAlign: "center" }}>{s.label}</div>
              </div>
            </React.Fragment>
          );
        })}
      </div>
      <div style={{ fontFamily: MONO, fontSize: 30, color: C.dim, opacity: usePop(40) }}>
        merge → live in <span style={{ color: C.mint, fontWeight: 700 }}>~{clock} s</span> · no polling, no waiting on CI
      </div>
    </AbsoluteFill>
  );
};

const Outro: React.FC = () => {
  const f = useCurrentFrame();
  const fadeOut = interpolate(f, [OUTRO - 25, OUTRO], [1, 0], { extrapolateLeft: "clamp" });
  return (
    <AbsoluteFill style={{ alignItems: "center", justifyContent: "center", gap: 30, opacity: fadeOut }}>
      <div style={{ fontSize: 96, color: C.purple, transform: `scale(${usePop(0, 10)})`, filter: `drop-shadow(0 0 30px ${C.violet})` }}>✦</div>
      <div style={{ fontFamily: MONO, fontWeight: 700, fontSize: 104, color: C.bright, opacity: usePop(6) }}>eldar-os</div>
      <div style={{ fontFamily: SANS, fontSize: 34, color: C.text, opacity: usePop(16) }}>NixOS · a Go supervisor · Claude Code agents in a microVM</div>
      <div style={{ marginTop: 18, padding: "16px 30px", borderRadius: 16, border: `1.5px solid ${C.purple}66`, background: "rgba(26,27,38,.8)",
        fontFamily: MONO, fontSize: 34, color: C.cyan, opacity: usePop(28), transform: `translateY(${(1 - usePop(28)) * 20}px)` }}>
        github.com/benjaminwood/eldar-os
      </div>
      <div style={{ fontFamily: SANS, fontSize: 22, color: C.dim, opacity: usePop(44), marginTop: 26 }}>Real software, recorded live. Made-up data.</div>
    </AbsoluteFill>
  );
};

const scene = (name: string): React.ReactNode => {
  switch (name) {
    case "intro":
      return <Intro />;
    case "approvals":
      return (
        <PhoneScene clip="approvals" kicker="Approvals" headline="Every credentialed command waits for you." accent={["waits", "you."]}>
          <Chip at={mark("approvals", "approve") + 8} until={mark("approvals", "arrive")} icon="✓">Approved once. The next one asks again.</Chip>
          <Chip at={mark("approvals", "arrive") + 6} until={mark("approvals", "deny")} icon="＋" tone={C.cyan}>A new request comes in, live…</Chip>
          <Chip at={mark("approvals", "deny") + 8} icon="✗" tone={C.red}>…and you can say no.</Chip>
        </PhoneScene>
      );
    case "grant":
      return (
        <TuiScene clip="grant" kicker="Grants" headline="Or give one session a scope, for a while." accent={["scope,", "while."]}
          zooms={[
            { at: mark("grant", "editor") + 6, scale: 1.55, x: 0.5, y: 0.42 },
            { at: mark("grant", "granted") + 10, scale: 1, x: 0.5, y: 0.5 },
            { at: mark("grant", "activity") + 8, scale: 1.45, x: 0.1, y: 0.12 },
          ]}>
          <Chip at={mark("grant", "editor") + 30} until={mark("grant", "activity")} leave icon="◐" tone={C.purple}>One session · GITHUB_TOKEN · no AWS · 1 hour</Chip>
          <Chip at={mark("grant", "activity") + 24} leave icon="⚡" tone={C.orange}>Its next GitHub command just runs. Everyone else still asks.</Chip>
        </TuiScene>
      );
    case "terminal":
      return (
        <PhoneScene clip="terminal" kicker="Sessions" headline="Step into any agent's terminal, from your phone." accent={["any", "phone."]}>
          <Chip at={12} until={mark("terminal", "terminal")} icon="▦" tone={C.cyan}>Many agents, one desk, in herdr.</Chip>
          <Chip at={mark("terminal", "terminal") + 20} until={mark("terminal", "tray")} icon="⏺" tone={C.teal}>The push went through; the PR opened under the grant.</Chip>
          <Chip at={mark("terminal", "tray") - 30} icon="⚠" tone={C.orange}>The grant doesn't cover AWS, so it asks. Approve it right there.</Chip>
        </PhoneScene>
      );
    case "chat":
      return (
        <PhoneScene clip="chat" kicker="Chief of staff" headline="Ask Eldar how it's going." accent={["Eldar"]}>
          <Chip at={mark("chat", "reply") + 12} icon="✦" tone={C.purple}>Eldar keeps track of the other agents, and relays them to you.</Chip>
        </PhoneScene>
      );
    case "audit":
      return (
        <TuiScene clip="audit" kicker="Audit" headline="Every decision, on the record." accent={["record."]}
          zooms={[{ at: 10, scale: 1.12, x: 0, y: 0 }]}>
          <Chip at={20} leave icon="≡" tone={C.cyan}>Who asked, what ran, who allowed it, and why.</Chip>
        </TuiScene>
      );
    case "pipeline":
      return <Pipeline />;
    default:
      return <Outro />;
  }
};

// Where each scene starts in the whole, for the backdrop's continuity.
const starts = scenes.map((_, i) => scenes.slice(0, i).reduce((n, [, d]) => n + d - T, 0));

const transition = (i: number) =>
  i % 2 === 0 ? slide({ direction: "from-right" }) : fade();

export const Promo: React.FC = () => (
  <AbsoluteFill style={{ background: C.bg }}>
    <TransitionSeries>
      {scenes.flatMap(([name, d], i) => [
        ...(i > 0 ? [<TransitionSeries.Transition key={`t${i}`} presentation={transition(i)} timing={springTiming({ config: { damping: 200 }, durationInFrames: T })} />] : []),
        <TransitionSeries.Sequence key={name} durationInFrames={d}>
          <AbsoluteFill>
            <Backdrop offset={starts[i]} />
            {scene(name)}
          </AbsoluteFill>
        </TransitionSeries.Sequence>,
      ])}
    </TransitionSeries>
  </AbsoluteFill>
);
