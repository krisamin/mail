import { reactRouter } from "@react-router/dev/vite";
import tailwindcss from "@tailwindcss/vite";
import { defineConfig } from "vite";

export default defineConfig({
  plugins: [tailwindcss(), reactRouter()],
  // Vite resolves the tsconfig "~/*" paths natively now, so no extra plugin.
  resolve: { tsconfigPaths: true },
  server: {
    host: "0.0.0.0",
    // 5173 is usually taken by another dev server on this machine.
    port: 5573,
  },
});
