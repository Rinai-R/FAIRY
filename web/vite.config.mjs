import { defineConfig } from "vite";

export default defineConfig({
  server: {
    proxy: {
      "/api": "http://127.0.0.1:8787",
      "/audio": "http://127.0.0.1:8787",
      "/images": "http://127.0.0.1:8787"
    }
  }
});
