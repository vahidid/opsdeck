import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

export default defineConfig({
  plugins: [react(), tailwindcss()],
  build: {
    outDir: "../internal/server/dist",
    emptyOutDir: true,
    chunkSizeWarningLimit: 2000,
  },
  server: {
    port: 5173,
    proxy: {
      "/api": { target: "http://127.0.0.1:7788", changeOrigin: false, ws: true },
    },
  },
});
