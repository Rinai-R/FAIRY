import { defineConfig } from "vite";

const apiProxy = process.env.FAIRY_API_PROXY || "http://127.0.0.1:8787";

export default defineConfig({
  server: {
    proxy: {
      "/api": apiProxy,
      "/audio": apiProxy,
      "/images": apiProxy
    }
  }
});
