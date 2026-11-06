// The app is a pure client-side SPA served by the Go binary: there's no
// server to render on, and every route depends on the session cookie,
// which only the browser has, so prerendering would just bake out an empty
// shell.
export const ssr = false;
export const prerender = false;
