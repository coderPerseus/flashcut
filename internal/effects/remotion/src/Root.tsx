import React from "react";
import { Composition } from "remotion";
import { FlashCut, type Plan } from "./FlashCut";

const fallback: Plan = {
  video: "cut.mp4",
  width: 1280,
  height: 720,
  fps: 30,
  duration: 10,
  outroSeconds: 2.5,
  title: "闪剪",
  subtitle: "",
  accent: "#7c5cff",
  style: "esports",
  intro: true,
  progressBar: true,
  events: [],
  stats: "",
};

export const Root: React.FC = () => (
  <Composition
    id="FlashCut"
    component={FlashCut}
    defaultProps={fallback}
    width={1280}
    height={720}
    fps={30}
    durationInFrames={300}
    calculateMetadata={({ props }) => ({
      width: props.width,
      height: props.height,
      fps: props.fps,
      durationInFrames: Math.max(1, Math.round((props.duration + (props.outroSeconds > 0 ? props.outroSeconds : 0)) * props.fps)),
    })}
  />
);
