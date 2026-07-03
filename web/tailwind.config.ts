import type { Config } from "tailwindcss";

export default {
  content: ["./app/**/*.{ts,tsx}"],
  theme: {
    extend: {
      colors: {
        bg: { 0: "#0a0a0b", 1: "#111113", 2: "#1a1a1d", 3: "#26262a" },
        line: "#2a2a2e",
        text: { 0: "#f5f5f7", 1: "#c8c8cf", 2: "#8b8b93" },
        muted: "#6b6b73",
        ok: "#7dd3a3",
        bad: "#ef6b6b",
        warn: "#f5cc6b",
        accent: { DEFAULT: "#8ab4f8", soft: "#8ab4f833", hover: "#a8c7fa" },
      },
    },
  },
  plugins: [],
} satisfies Config;
