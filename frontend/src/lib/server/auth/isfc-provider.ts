import { readIsfcConfig, type IsfcConfig } from './config';
import { fetchLoginMethods, METHOD_LABELS, type Fetch } from './isfc';
import type { AuthProvider, LoginView } from './provider';

/**
 * The ISFC provider (HUB-491, AUTH_PLAN §9). The login page lists the methods ISFC has active for
 * the console client (`GET {auth}/oauth2/method/<client>`), and offers only Email OTP and
 * Microsoft. Starting a sign-in is `login-flow.ts` (`startLogin`); the form action and
 * `/auth/callback` call it, so `signIn` has nothing to do here.
 */
export function isfcProvider(
	deps: { fetch?: Fetch; config?: IsfcConfig | null } = {}
): AuthProvider {
	return {
		id: 'isfc',
		async describe(): Promise<LoginView> {
			const base = { provider: 'isfc' as const, methods: [] };
			const cfg = deps.config === undefined ? readIsfcConfig() : null;
			const config = deps.config ?? (cfg && cfg.ok ? cfg.config : null);
			if (!config) {
				return {
					...base,
					ready: false,
					notice: 'Sign-in with ISFC is not configured on this server.',
					missing: cfg && !cfg.ok ? cfg.missing : []
				};
			}
			try {
				const methods = (await fetchLoginMethods(config, deps.fetch ?? fetch)).map((m) => ({
					id: m.id,
					label: METHOD_LABELS[m.id]
				}));
				return methods.length
					? { ...base, ready: true, notice: null, missing: [], methods }
					: {
							...base,
							ready: false,
							notice:
								'ISFC has no sign-in method (Email OTP or Microsoft) active for the Integration Hub console.',
							missing: []
						};
			} catch {
				return {
					...base,
					ready: false,
					notice:
						'ISFC did not answer, so the sign-in methods could not be loaded. Try again in a moment.',
					missing: []
				};
			}
		},
		async signIn() {
			return { ok: false, status: 400, message: 'Choose Email OTP or Microsoft.' };
		}
	};
}
