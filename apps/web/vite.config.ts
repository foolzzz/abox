import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import { VitePWA } from "vite-plugin-pwa";

export default defineConfig({
  plugins: [
    react(),
    VitePWA({
      registerType: "autoUpdate",
      injectRegister: null,
      includeAssets: ["app-icon.svg"],
      manifest: {
        name: "AgentBox Console",
        short_name: "AgentBox",
        description: "Operate agents, hosts, workspaces, and live boxes.",
        theme_color: "#0b0f17",
        background_color: "#0b0f17",
        display: "standalone",
        orientation: "any",
        start_url: "/",
        scope: "/",
        icons: [
          {
            src: "/app-icon.svg",
            sizes: "any",
            type: "image/svg+xml",
            purpose: "any maskable"
          }
        ]
      },
      workbox: {
        navigateFallback: "/index.html",
        navigateFallbackDenylist: [/^\/api\/v1(?:\/|$)/],
        runtimeCaching: [
          {
            urlPattern: /\/api\/v1\//,
            handler: "NetworkOnly"
          }
        ]
      }
    })
  ],
  server: {
    proxy: {
      "/api": {
        target: "http://127.0.0.1:8080",
        changeOrigin: false
      }
    }
  },
  build: {
    sourcemap: true,
    target: "es2022"
  }
});
