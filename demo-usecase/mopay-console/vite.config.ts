import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// NFR-2: the console must load in under 2s on a 3G connection. No chart library, no UI
// framework, no router - the sparkline is inline SVG and routing is component state.
export default defineConfig({
  // The console is served behind a path prefix, not at the origin root: every
  // `managed-service` component in an environment shares one hostname and is
  // separated by /{component}-{endpoint}. Without this, index.html would ask for
  // /assets/... at the origin root, where no route matches.
  //
  // This is a CONSTANT, not an environment-specific value — the prefix is the same
  // in development as in prod-ng — so hard constraint 5 still holds and one image
  // still promotes unchanged.
  base: '/mopay-console-webui/',
  plugins: [react()],
  server: { host: '0.0.0.0', port: 5173 },
  build: { target: 'es2022' },
})
