/// <reference types="vite/client" />

// Dev-only. In a deployed container these arrive at runtime via /config.js - see config.ts.
interface ImportMetaEnv {
  readonly VITE_API_BASE_URL: string
  readonly VITE_MERCHANT_API_BASE_URL: string
}
interface ImportMeta {
  readonly env: ImportMetaEnv
}
