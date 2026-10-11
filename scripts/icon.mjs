// Renders public/logo.svg to the app icon and the website images.
//   node scripts/icon.mjs
import { Resvg } from "@resvg/resvg-js";
import { readFileSync, writeFileSync, mkdirSync } from "node:fs";

const svg = readFileSync(new URL("../public/logo.svg", import.meta.url), "utf8");
const render = (size) => new Resvg(svg, { fitTo: { mode: "width", value: size }, background: "rgba(0,0,0,0)" }).render().asPng();

mkdirSync(new URL("../resources/", import.meta.url), { recursive: true });
writeFileSync(new URL("../resources/icon.png", import.meta.url), render(1024));
mkdirSync(new URL("../site/", import.meta.url), { recursive: true });
writeFileSync(new URL("../site/icon-512.png", import.meta.url), render(512));
writeFileSync(new URL("../site/favicon.png", import.meta.url), render(64));
console.log("wrote resources/icon.png, site/icon-512.png, site/favicon.png");
