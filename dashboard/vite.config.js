import { defineConfig } from 'vite';

const backend = process.env.ACTSPEED_BACKEND || 'http://localhost:8080';
const proxy = {
  '/v1': { target: backend, ws: true },
  '/healthz': backend,
};
export default defineConfig({ server: { proxy }, preview: { proxy } });
