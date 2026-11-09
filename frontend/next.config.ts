import type { NextConfig } from "next";

const config: NextConfig = {
  env: {
    NEXT_PUBLIC_ENGINE_HTTP: process.env.NEXT_PUBLIC_ENGINE_HTTP ?? "http://localhost:8080",
    NEXT_PUBLIC_ENGINE_WS: process.env.NEXT_PUBLIC_ENGINE_WS ?? "ws://localhost:8080/ws",
  },
};

export default config;
