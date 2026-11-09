<script lang="ts">
	import type { Snippet } from 'svelte';

	// Field owns the <label for=id>; children renders the actual input so
	// every input in the app has a real, associated label (never a
	// placeholder standing in for one).
	let {
		label,
		id,
		hint,
		error,
		children
	}: {
		label: string;
		id: string;
		hint?: string;
		error?: string;
		children: Snippet<[{ describedBy: string | undefined; invalid: boolean }]>;
	} = $props();
	let container: HTMLDivElement;
	$effect(() => {
		const control = [...(container?.querySelectorAll<HTMLElement>('input, select, textarea') ?? [])].find((element) => element.id === id);
		if (!control) return;
		const descriptions = (control.getAttribute('aria-describedby') ?? '').split(/\s+/).filter((value) => value && value !== `${id}-hint` && value !== `${id}-error`);
		if (error) descriptions.push(`${id}-error`);
		else if (hint) descriptions.push(`${id}-hint`);
		if (descriptions.length) control.setAttribute('aria-describedby', descriptions.join(' '));
		else control.removeAttribute('aria-describedby');
		if (error) control.setAttribute('aria-invalid', 'true');
		else control.removeAttribute('aria-invalid');
	});
</script>

<div bind:this={container} class="fieldset py-0">
	<label for={id} class="fieldset-legend pb-0">{label}</label>
	{@render children({ describedBy: error ? `${id}-error` : hint ? `${id}-hint` : undefined, invalid: !!error })}
	{#if error}
		<p id={`${id}-error`} class="m-0 text-sm text-error" role="alert">{error}</p>
	{:else if hint}
		<p id={`${id}-hint`} class="m-0 text-sm text-base-content/70">{hint}</p>
	{/if}
</div>
