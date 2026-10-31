export type ThemePreference = 'system' | 'light' | 'dark';
export type Theme = 'light' | 'dark';

/** localStorage key; also read by the no-flash inline script in src/app.html (keep in sync). */
export const THEME_STORAGE_KEY = 'hub-console:theme';
const PREFERENCES: readonly ThemePreference[] = ['system', 'light', 'dark'];

export function isThemePreference(v: unknown): v is ThemePreference {
	return typeof v === 'string' && (PREFERENCES as readonly string[]).includes(v);
}

export function resolveTheme(pref: ThemePreference, systemPrefersDark: boolean): Theme {
	if (pref === 'system') return systemPrefersDark ? 'dark' : 'light';
	return pref;
}

/** The operator's theme choice (a UI preference, safe for localStorage; never secrets). */
class ThemeState {
	preference = $state<ThemePreference>('system');
	systemPrefersDark = $state(false);
	readonly theme = $derived(resolveTheme(this.preference, this.systemPrefersDark));

	/** Reads the stored choice and follows OS changes. Returns a cleanup. Browser only. */
	init(): () => void {
		try {
			const stored = localStorage.getItem(THEME_STORAGE_KEY);
			if (isThemePreference(stored)) this.preference = stored;
		} catch {
			/* storage blocked: keep "system" */
		}
		const mq = matchMedia('(prefers-color-scheme: dark)');
		this.systemPrefersDark = mq.matches;
		const onChange = (e: MediaQueryListEvent) => (this.systemPrefersDark = e.matches);
		mq.addEventListener('change', onChange);
		return () => mq.removeEventListener('change', onChange);
	}

	set(pref: ThemePreference): void {
		this.preference = pref;
		try {
			localStorage.setItem(THEME_STORAGE_KEY, pref);
		} catch {
			/* storage blocked: preference lasts for this page only */
		}
	}
}

export const themeState = new ThemeState();
