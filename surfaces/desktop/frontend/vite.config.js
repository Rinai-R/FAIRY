import path from "node:path";
import { fileURLToPath } from "node:url";
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

const webRoot = path.dirname(fileURLToPath(import.meta.url));
const surfaceRoot = path.resolve(webRoot, "..");

// wails3 dev 默认在 9245 上代理前端 dev server（见 build/config.yml 的 dev_mode）。
// 端口需与 wails3 的 devServerURL 一致，否则会 "unable to connect to frontend server"。
const devServerPort = Number(process.env.FRONTEND_PORT) || 9245;

// 生成的 bindings 从 /wails/runtime.js 导入。该路径在生产由 wails AssetServer 运行时提供，
// 不存在于前端源码里：
//   - build：用 rollup external 排除，运行时由 wails 提供。
//   - dev：rollup external 不生效，且 vite 会尝试加载该路径而失败，
//           因此别名到已安装的 @wailsio/runtime 包（与之等价），让 vite 正常解析。
export default defineConfig(({ command }) => ({
  plugins: [react()],
  clearScreen: false,
  resolve: {
    alias: command === "serve" ? { "/wails/runtime.js": "@wailsio/runtime" } : {},
  },
  build: {
    rollupOptions: {
      external: ["/wails/runtime.js"],
    },
  },
  server: {
    host: "127.0.0.1",
    port: devServerPort,
    strictPort: true,
    fs: {
      allow: [surfaceRoot],
    },
  },
}));
