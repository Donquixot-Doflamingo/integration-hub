// Mock mode renders client-side only: MSW runs in the browser (service worker), so there is no
// server-side fetch to intercept. Normal mode keeps SSR on (internal console, session cookie).
export const ssr = !__API_MOCKING__;
