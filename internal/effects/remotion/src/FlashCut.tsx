import React from "react";
import {
  AbsoluteFill,
  Easing,
  interpolate,
  OffthreadVideo,
  random,
  Sequence,
  spring,
  staticFile,
  useCurrentFrame,
  useVideoConfig,
} from "remotion";

export type EffectEvent = {
  kind: "transition" | "highlight" | "opening" | "ending";
  /** Seconds on the output timeline. */
  start: number;
  end: number;
  label: string;
};

export type Plan = {
  video: string;
  width: number;
  height: number;
  fps: number;
  /** Length of the cut video in seconds. */
  duration: number;
  /** End card shown after the video, 0 for none. */
  outroSeconds: number;
  title: string;
  subtitle: string;
  accent: string;
  style: "esports" | "clean";
  intro: boolean;
  progressBar: boolean;
  events: EffectEvent[];
  stats: string;
};

const FONT = '"PingFang SC", "Hiragino Sans GB", "Noto Sans CJK SC", system-ui, sans-serif';

export const FlashCut: React.FC<Plan> = (plan) => {
  const frame = useCurrentFrame();
  const { fps } = useVideoConfig();
  const t = frame / fps;
  const videoFrames = Math.round(plan.duration * fps);
  const { scale, x, y } = cameraAt(plan, t, fps);

  return (
    <AbsoluteFill style={{ backgroundColor: "#000", fontFamily: FONT }}>
      <Sequence durationInFrames={videoFrames}>
        <AbsoluteFill style={{ transform: `translate(${x}px, ${y}px) scale(${scale})` }}>
          <OffthreadVideo src={staticFile(plan.video)} />
        </AbsoluteFill>
        {plan.events.map((e, i) => (
          <EventLayer key={i} event={e} plan={plan} />
        ))}
        {plan.progressBar && <Progress plan={plan} />}
      </Sequence>
      {plan.intro && (
        <Sequence durationInFrames={Math.round(2.6 * fps)}>
          <Intro plan={plan} />
        </Sequence>
      )}
      {plan.outroSeconds > 0 && (
        <Sequence from={videoFrames} durationInFrames={Math.round(plan.outroSeconds * fps)}>
          <Outro plan={plan} />
        </Sequence>
      )}
    </AbsoluteFill>
  );
};

/** cameraAt adds the punch-in, bump and shake of nearby events. */
function cameraAt(plan: Plan, t: number, fps: number) {
  let scale = 1;
  let x = 0;
  let y = 0;
  for (const e of plan.events) {
    const dt = t - e.start;
    if (e.kind === "highlight" && dt >= 0 && dt < 1.4) {
      // Fast push in, slow settle.
      scale *= interpolate(dt, [0, 0.18, 1.4], [1, 1.09, 1], { easing: Easing.out(Easing.cubic), extrapolateRight: "clamp" });
      if (dt < 0.45) {
        const amp = interpolate(dt, [0, 0.45], [plan.width * 0.012, 0], { extrapolateRight: "clamp" });
        const f = Math.floor(dt * fps);
        x += (random(`x${e.start}${f}`) - 0.5) * 2 * amp;
        y += (random(`y${e.start}${f}`) - 0.5) * 2 * amp;
      }
    }
    if (e.kind === "transition" && Math.abs(dt) < 0.3) {
      scale *= interpolate(dt, [-0.3, 0, 0.3], [1, 1.05, 1], { easing: Easing.inOut(Easing.quad) });
    }
  }
  return { scale, x, y };
}

const EventLayer: React.FC<{ event: EffectEvent; plan: Plan }> = ({ event, plan }) => {
  const { fps } = useVideoConfig();
  const from = Math.max(0, Math.round((event.start - (event.kind === "transition" ? 0.3 : 0)) * fps));
  const len = Math.max(1, Math.round((event.end - event.start + (event.kind === "transition" ? 0.6 : 0)) * fps));
  return (
    <Sequence from={from} durationInFrames={len} layout="none">
      {event.kind === "transition" && <Transition plan={plan} />}
      {event.kind === "highlight" && <Highlight plan={plan} label={event.label} seconds={event.end - event.start} />}
      {event.kind === "ending" && <Ending plan={plan} label={event.label} />}
    </Sequence>
  );
};

const Transition: React.FC<{ plan: Plan }> = ({ plan }) => {
  const frame = useCurrentFrame();
  const { fps, width } = useVideoConfig();
  const dt = frame / fps - 0.3; // 0 at the cut
  const flash = interpolate(dt, [-0.12, 0, 0.25], [0, plan.style === "esports" ? 0.75 : 0.45, 0], {
    extrapolateLeft: "clamp",
    extrapolateRight: "clamp",
  });
  const sweep = interpolate(dt, [-0.25, 0.25], [-0.6, 1.6], { extrapolateLeft: "clamp", extrapolateRight: "clamp" });
  return (
    <AbsoluteFill style={{ pointerEvents: "none" }}>
      <AbsoluteFill style={{ backgroundColor: "#fff", opacity: flash }} />
      {plan.style === "esports" && (
        <div
          style={{
            position: "absolute",
            top: "-20%",
            bottom: "-20%",
            left: sweep * width,
            width: width * 0.18,
            transform: "skewX(-18deg)",
            background: `linear-gradient(90deg, transparent, ${plan.accent}cc, #ffffffcc, ${plan.accent}cc, transparent)`,
            filter: "blur(2px)",
          }}
        />
      )}
    </AbsoluteFill>
  );
};

const Highlight: React.FC<{ plan: Plan; label: string; seconds: number }> = ({ plan, label, seconds }) => {
  const frame = useCurrentFrame();
  const { fps, width, height } = useVideoConfig();
  const t = frame / fps;
  const shown = Math.min(seconds, 4);
  const pop = spring({ frame, fps, config: { damping: 12, stiffness: 180 } });
  const out = interpolate(t, [shown - 0.3, shown], [1, 0], { extrapolateLeft: "clamp", extrapolateRight: "clamp" });
  const glow = interpolate(t, [0, 0.15, 1.2], [0, 1, 0], { extrapolateRight: "clamp" });
  const unit = width / 1280;
  return (
    <AbsoluteFill style={{ pointerEvents: "none" }}>
      {/* Edge glow on impact. */}
      <AbsoluteFill style={{ boxShadow: `inset 0 0 ${180 * unit}px ${40 * unit}px ${plan.accent}`, opacity: glow * 0.85 }} />
      {/* Speed lines for the esports look. */}
      {plan.style === "esports" && t < 0.6 && <SpeedLines plan={plan} t={t} />}
      <div
        style={{
          // Top center, below where scoreboards usually sit.
          position: "absolute",
          top: height * 0.14,
          left: "50%",
          transform: `translateX(-50%) scale(${pop}) rotate(${(1 - pop) * -6}deg)`,
          transformOrigin: "center top",
          opacity: out,
          display: "flex",
          alignItems: "center",
          gap: 10 * unit,
          padding: `${10 * unit}px ${20 * unit}px`,
          borderRadius: (plan.style === "esports" ? 6 : 999) * unit,
          background: plan.style === "esports" ? `linear-gradient(110deg, ${plan.accent}, #ff3d6e)` : "rgba(0,0,0,0.55)",
          color: "#fff",
          fontWeight: 900,
          fontSize: 34 * unit,
          letterSpacing: 2 * unit,
          boxShadow: `0 ${8 * unit}px ${30 * unit}px rgba(0,0,0,0.45)`,
          fontStyle: plan.style === "esports" ? "italic" : "normal",
        }}
      >
        <span style={{ fontSize: 30 * unit }}>⚡</span>
        <span>高能</span>
        {label && <span style={{ fontWeight: 600, fontSize: 24 * unit, opacity: 0.92 }}>· {label}</span>}
      </div>
    </AbsoluteFill>
  );
};

const SpeedLines: React.FC<{ plan: Plan; t: number }> = ({ plan, t }) => {
  const { width, height } = useVideoConfig();
  const o = interpolate(t, [0, 0.1, 0.6], [0, 0.8, 0], { extrapolateRight: "clamp" });
  const lines = Array.from({ length: 28 }, (_, i) => {
    const a = (i / 28) * Math.PI * 2 + random(`a${i}`) * 0.2;
    const r0 = 0.42 + random(`r${i}`) * 0.12;
    return { a, r0, w: 2 + random(`w${i}`) * 5 };
  });
  const cx = width / 2;
  const cy = height / 2;
  const R = Math.hypot(cx, cy);
  return (
    <svg width={width} height={height} style={{ position: "absolute", inset: 0, opacity: o }}>
      {lines.map((l, i) => (
        <line
          key={i}
          x1={cx + Math.cos(l.a) * R * l.r0}
          y1={cy + Math.sin(l.a) * R * l.r0}
          x2={cx + Math.cos(l.a) * R * 1.1}
          y2={cy + Math.sin(l.a) * R * 1.1}
          stroke={i % 3 === 0 ? plan.accent : "#ffffff"}
          strokeWidth={l.w}
          strokeLinecap="round"
        />
      ))}
    </svg>
  );
};

const Intro: React.FC<{ plan: Plan }> = ({ plan }) => {
  const frame = useCurrentFrame();
  const { fps, width, height } = useVideoConfig();
  const t = frame / fps;
  const unit = width / 1280;
  const fade = interpolate(t, [2.1, 2.6], [1, 0], { extrapolateLeft: "clamp", extrapolateRight: "clamp" });
  const title = spring({ frame: frame - 4, fps, config: { damping: 14 } });
  const bar = interpolate(t, [0, 0.5], [0, 1], { extrapolateRight: "clamp", easing: Easing.out(Easing.cubic) });
  const sub = interpolate(t, [0.45, 0.8], [0, 1], { extrapolateLeft: "clamp", extrapolateRight: "clamp" });
  return (
    <AbsoluteFill style={{ opacity: fade, justifyContent: "center", alignItems: "center" }}>
      <AbsoluteFill
        style={{
          background:
            plan.style === "esports"
              ? `radial-gradient(circle at 50% 50%, rgba(10,8,30,0.55), rgba(5,5,12,0.92)), linear-gradient(120deg, ${plan.accent}55, transparent 60%)`
              : "rgba(0,0,0,0.6)",
        }}
      />
      <div style={{ position: "relative", textAlign: "center", color: "#fff", width: width * 0.8 }}>
        <div
          style={{
            height: 6 * unit,
            width: `${bar * 46}%`,
            margin: `0 auto ${22 * unit}px`,
            background: plan.accent,
            borderRadius: 3 * unit,
            boxShadow: `0 0 ${24 * unit}px ${plan.accent}`,
          }}
        />
        <div
          style={{
            fontSize: Math.min(76 * unit, (width * 0.8) / Math.max(4, plan.title.length) * 1.6),
            fontWeight: 900,
            letterSpacing: 4 * unit,
            transform: `translateY(${(1 - title) * 40 * unit}px) scale(${0.9 + title * 0.1})`,
            opacity: title,
            fontStyle: plan.style === "esports" ? "italic" : "normal",
            textShadow: `0 ${4 * unit}px ${30 * unit}px rgba(0,0,0,0.6)`,
          }}
        >
          {plan.title}
        </div>
        {plan.subtitle && (
          <div style={{ marginTop: 18 * unit, fontSize: 26 * unit, opacity: sub * 0.9, letterSpacing: 6 * unit }}>{plan.subtitle}</div>
        )}
      </div>
      {plan.style === "esports" && (
        <div
          style={{
            position: "absolute",
            bottom: height * 0.12,
            left: 0,
            right: 0,
            textAlign: "center",
            color: plan.accent,
            fontWeight: 800,
            fontSize: 20 * unit,
            letterSpacing: 12 * unit,
            opacity: sub,
          }}
        >
          HIGHLIGHTS
        </div>
      )}
    </AbsoluteFill>
  );
};

const Ending: React.FC<{ plan: Plan; label: string }> = ({ plan, label }) => {
  const frame = useCurrentFrame();
  const { fps, width, height } = useVideoConfig();
  const unit = width / 1280;
  const s = spring({ frame, fps, config: { damping: 16 } });
  return (
    <AbsoluteFill style={{ pointerEvents: "none" }}>
      <div
        style={{
          position: "absolute",
          left: width * 0.04,
          bottom: height * 0.1,
          transform: `translateX(${(1 - s) * -60 * unit}px)`,
          opacity: s,
          color: "#fff",
          background: "rgba(0,0,0,0.55)",
          borderLeft: `${6 * unit}px solid ${plan.accent}`,
          padding: `${10 * unit}px ${20 * unit}px`,
          fontSize: 30 * unit,
          fontWeight: 800,
        }}
      >
        {label || "终局时刻"}
      </div>
    </AbsoluteFill>
  );
};

const Outro: React.FC<{ plan: Plan }> = ({ plan }) => {
  const frame = useCurrentFrame();
  const { fps, width } = useVideoConfig();
  const unit = width / 1280;
  const s = spring({ frame, fps, config: { damping: 15 } });
  return (
    <AbsoluteFill
      style={{
        justifyContent: "center",
        alignItems: "center",
        color: "#fff",
        background: plan.style === "esports" ? `radial-gradient(circle, #1a1440, #050508)` : "#0b0b0e",
      }}
    >
      <div style={{ textAlign: "center", transform: `scale(${0.85 + s * 0.15})`, opacity: s }}>
        <div style={{ fontSize: 70 * unit, fontWeight: 900, fontStyle: plan.style === "esports" ? "italic" : "normal" }}>
          {plan.style === "esports" ? "GG" : "完"}
        </div>
        <div style={{ marginTop: 12 * unit, fontSize: 30 * unit, fontWeight: 700 }}>{plan.title}</div>
        {plan.stats && <div style={{ marginTop: 16 * unit, fontSize: 22 * unit, opacity: 0.75 }}>{plan.stats}</div>}
        <div style={{ marginTop: 36 * unit, fontSize: 16 * unit, color: plan.accent, letterSpacing: 4 * unit }}>由 闪剪 生成</div>
      </div>
    </AbsoluteFill>
  );
};

const Progress: React.FC<{ plan: Plan }> = ({ plan }) => {
  const frame = useCurrentFrame();
  const { fps, width } = useVideoConfig();
  const p = Math.min(1, frame / (plan.duration * fps));
  return (
    <div
      style={{
        position: "absolute",
        left: 0,
        bottom: 0,
        height: Math.max(3, 4 * (width / 1280)),
        width: `${p * 100}%`,
        background: plan.accent,
        boxShadow: `0 0 8px ${plan.accent}`,
      }}
    />
  );
};
