// Renders the FlashCut composition.
//   node render.mjs <plan.json> <publicDir> <out.mp4>
// Prints one JSON object per line: {"stage":"bundle"|"render","progress":0..1}.
import { bundle } from "@remotion/bundler";
import { renderMedia, selectComposition, ensureBrowser } from "@remotion/renderer";
import { readFileSync } from "node:fs";
import { cpus } from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

const [planPath, publicDir, out] = process.argv.slice(2);
if (!planPath || !publicDir || !out) {
  console.error("usage: node render.mjs <plan.json> <publicDir> <out.mp4>");
  process.exit(2);
}
const plan = JSON.parse(readFileSync(planPath, "utf8"));
const here = path.dirname(fileURLToPath(import.meta.url));
const say = (o) => process.stdout.write(JSON.stringify(o) + "\n");

say({ stage: "browser", progress: 0 });
await ensureBrowser();
say({ stage: "bundle", progress: 0 });
const serveUrl = await bundle({
  entryPoint: path.join(here, "src/index.ts"),
  publicDir,
  onProgress: (p) => say({ stage: "bundle", progress: p / 100 }),
});
const composition = await selectComposition({ serveUrl, id: "FlashCut", inputProps: plan });
let last = -1;
await renderMedia({
  composition,
  serveUrl,
  codec: "h264",
  crf: 20,
  outputLocation: out,
  inputProps: plan,
  concurrency: Math.max(1, Math.floor(cpus().length / 2)),
  onProgress: ({ progress }) => {
    const p = Math.floor(progress * 100);
    if (p !== last) {
      last = p;
      say({ stage: "render", progress });
    }
  },
});
say({ stage: "done", progress: 1 });
