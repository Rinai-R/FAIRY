import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

export default defineConfig({
  plugins: [react()],
  base: "/console/",
  build: {
    outDir: "../fairy/api/console/dist",
    emptyOutDir: true,
  },
  server: {
    proxy: {
      "/v1": "http://127.0.0.1:8787",
    },
  },
});
