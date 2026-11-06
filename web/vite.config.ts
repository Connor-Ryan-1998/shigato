import adapter from '@sveltejs/adapter-static';
import { sveltekit } from '@sveltejs/kit/vite';
import type { ProxyOptions } from 'vite';
import { defineConfig } from 'vitest/config';

// The dev server proxies /api and /webhooks to the Go backend. The backend's
// CSRF middleware (internal/httpapi/csrf.go) compares a request's Origin
// header against Config.BaseURL's origin, and rejects a mismatch. In dev the
// browser sends Origin: http://localhost:5173 (Vite's own port), which would
// never match http://localhost:8080 (the backend's BaseURL), so every
// unsafe request would be rejected. Rewriting the proxied request's Origin
// to the backend's own origin fixes this without any server-side dev bypass
// (see SDD ruling 40).
const backend = 'http://localhost:8080';

function rewriteOrigin(proxy: ProxyOptions): ProxyOptions {
	return {
		...proxy,
		configure(server) {
			server.on('proxyReq', (proxyReq) => {
				proxyReq.setHeader('origin', backend);
			});
		}
	};
}

export default defineConfig({
	plugins: [
		sveltekit({
			// Built as a plain SPA and served by the Go binary
			// (internal/webui), not as statically pre-rendered pages: every
			// route falls back to index.html, and client-side routing takes
			// it from there.
			adapter: adapter({
				pages: '../internal/webui/dist',
				assets: '../internal/webui/dist',
				fallback: 'index.html',
				strict: false
			})
		})
	],
	server: {
		proxy: {
			'/api': rewriteOrigin({ target: backend, changeOrigin: true }),
			'/webhooks': rewriteOrigin({ target: backend, changeOrigin: true })
		}
	},
	test: {
		environment: 'jsdom',
		include: ['src/**/*.test.ts']
	}
});
