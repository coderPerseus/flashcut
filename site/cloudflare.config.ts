import { defineConfig, triggers } from "cf/config";

// The website, served as static assets from a Worker at cut.islumi.com.
export default defineConfig({
  worker: {
    name: "flashcut-site",
    compatibilityDate: "2026-09-25",
    observability: { enabled: true },
    triggers: [triggers.fetch({ pattern: "cut.islumi.com/*", zone: "islumi.com" })],
  },
});
