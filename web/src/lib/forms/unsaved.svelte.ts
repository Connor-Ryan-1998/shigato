import { beforeNavigate } from '$app/navigation';
import { onMount, onDestroy } from 'svelte';
import { session } from '../session.svelte';
import { canDismiss } from './dismiss';

const guards = new Map<symbol, { dirty: () => boolean; busy: () => boolean }>();

export function useUnsavedChanges(isDirty: () => boolean, isBusy: () => boolean = () => false) {
	const id = Symbol();
	guards.set(id, { dirty: isDirty, busy: isBusy });
	onDestroy(() => guards.delete(id));
	const first = () => guards.keys().next().value === id;
	function allowLeave() {
		return canDismiss(isBusy(), isDirty());
	}
	beforeNavigate((navigation) => {
		// Authentication redirects and logout must always be able to leave.
		if (!first() || !session.me || navigation.to?.url.pathname === '/login') return;
		// A page and a nested dialog may both have edits; ask once for the whole navigation.
		if (!canDismiss([...guards.values()].some((guard) => guard.busy()), [...guards.values()].some((guard) => guard.dirty()))) navigation.cancel();
	});
	onMount(() => {
		const unload = (event: BeforeUnloadEvent) => {
			if (first() && session.me && [...guards.values()].some((guard) => guard.dirty() || guard.busy())) {
				event.preventDefault();
				event.returnValue = '';
			}
		};
		window.addEventListener('beforeunload', unload);
		return () => window.removeEventListener('beforeunload', unload);
	});
	return { allowLeave };
}
