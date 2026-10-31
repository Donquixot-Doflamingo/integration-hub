// The login page never calls the admin API, so it renders on the server even in mock mode
// (the root layout turns SSR off there because MSW only runs in the browser). That keeps the
// no-JS form post working: a failed sign-in re-renders with the posted values and the error.
export const ssr = true;
