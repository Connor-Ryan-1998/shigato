// theme is shared across the whole app (the root layout applies it so
// /login respects it too, not just the org shell where the toggle lives),
// so - like session.svelte.ts - it's a single module-level instance rather
// than per-component state.

export type Theme = 'system' | 'light' | 'dark';

const STORAGE_KEY = 'shigoto-theme';

function readStoredTheme(): Theme {
	try {
		const saved = localStorage.getItem(STORAGE_KEY);
		if (saved === 'light' || saved === 'dark') return saved;
	} catch {
		// Private browsing or blocked storage: fall back to system.
	}
	return 'system';
}

class ThemeStore {
	current = $state<Theme>(readStoredTheme());

	// apply reflects `current` onto <html> and persists the manual override
	// (or clears it, for 'system'). The root layout calls this once on
	// mount and again on every change.
	apply(): void {
		if (typeof document === 'undefined') return;
		if (this.current === 'system') {
			document.documentElement.removeAttribute('data-theme');
		} else {
			document.documentElement.setAttribute('data-theme', this.current);
		}
		try {
			if (this.current === 'system') {
				localStorage.removeItem(STORAGE_KEY);
			} else {
				localStorage.setItem(STORAGE_KEY, this.current);
			}
		} catch {
			// Private browsing or blocked storage: the toggle still works
			// for this page load, it just won't persist.
		}
	}

	cycle(): void {
		this.current = this.current === 'system' ? 'light' : this.current === 'light' ? 'dark' : 'system';
		this.apply();
	}
}

export const theme = new ThemeStore();
