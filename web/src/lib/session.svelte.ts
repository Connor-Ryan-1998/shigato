// session holds the GET /api/v1/me result, the enabled module list from
// GET /api/v1/meta, and which org the shell is currently showing. It's a
// single module-level instance (runes work at module scope in a .svelte.ts
// file) so the root layout, the shell and every page read the same state
// instead of each re-fetching it.

export type MeOrg = {
	slug: string;
	name: string;
	role: string;
	perms: string[];
};

export type MeResponse = {
	user: { id: number; email: string; name: string };
	orgs: MeOrg[];
};

class SessionStore {
	me = $state<MeResponse | null>(null);
	modules = $state<string[]>([]);
	orgSlug = $state<string | null>(null);

	get currentOrg(): MeOrg | null {
		if (!this.me || !this.orgSlug) return null;
		return this.me.orgs.find((o) => o.slug === this.orgSlug) ?? null;
	}

	setMe(me: MeResponse): void {
		this.me = me;
	}

	setModules(modules: string[]): void {
		this.modules = modules;
	}

	setOrg(slug: string): void {
		this.orgSlug = slug;
	}

	clear(): void {
		this.me = null;
		this.modules = [];
		this.orgSlug = null;
	}
}

export const session = new SessionStore();
