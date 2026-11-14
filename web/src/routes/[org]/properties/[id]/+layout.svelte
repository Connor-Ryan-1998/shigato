<script lang="ts">
	import type { Snippet } from 'svelte';
	import { page } from '$app/state';
	import { api } from '../../../../lib/api';
	import { session } from '../../../../lib/session.svelte';
	import PageHeader from '../../../../lib/components/PageHeader.svelte';
	import { propertyHeader } from '../../../../lib/modules/property/header.svelte';

	let { children }: { children: Snippet } = $props();

	const org = $derived(page.params.org ?? '');
	const id = $derived(page.params.id ?? '');
	const key = $derived(`${org}/${id}`);
	const canRead = $derived(session.currentOrg?.slug === org && (session.currentOrg?.perms.includes('property_read') ?? false));
	const base = $derived(`/${org}/properties/${id}`);
	const sections = $derived([
		{ href: base, label: 'Details' },
		{ href: `${base}/bills`, label: 'Bills' },
		{ href: `${base}/depreciation`, label: 'Depreciation' },
		{ href: `${base}/notes`, label: 'Notes' }
	]);
	const path = $derived(page.url?.pathname ?? '');
	const current = (href: string) => path === href || (href !== base && path.startsWith(`${href}/`));

	let seq = 0;
	$effect(() => {
		const k = key;
		if (!canRead || !org || !id) return;
		const token = ++seq;
		if (propertyHeader.key !== k) { propertyHeader.key = k; propertyHeader.name = ''; }
		api<{ name: string }>('GET', `/api/v1/orgs/${org}/properties/${id}`)
			.then((p) => { if (token === seq && propertyHeader.key === k) propertyHeader.name = p.name; })
			.catch(() => {});
		return () => { seq++; };
	});
	const title = $derived(propertyHeader.key === key && propertyHeader.name ? propertyHeader.name : 'Property');
</script>

<PageHeader {title} back={{ href: `/${org}/properties`, label: 'Back to properties' }} />
<nav aria-label="Property sections" class="tabs tabs-border mb-4">
	{#each sections as s (s.href)}
		<a class={['tab', current(s.href) && 'tab-active']} href={s.href} aria-current={current(s.href) ? 'page' : undefined}>{s.label}</a>
	{/each}
</nav>
{@render children()}
