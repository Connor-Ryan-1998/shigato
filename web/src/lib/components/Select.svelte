<script lang="ts">
	let {
		id,
		value = $bindable(),
		options,
		disabled = false,
		onchange
	}: {
		id: string;
		value: string;
		options: { value: string; label: string }[];
		disabled?: boolean;
		onchange?: (value: string) => void;
	} = $props();
</script>

<select
	{id}
	bind:value
	{disabled}
	onchange={() => onchange?.(value)}
>
	{#each options as opt (opt.value)}
		<option value={opt.value}>{opt.label}</option>
	{/each}
</select>

<style>
	select {
		padding: var(--space-2) var(--space-3);
		border-radius: var(--radius-md);
		border: 1px solid var(--border);
		background: var(--surface);
		color: var(--text);
		font: inherit;
	}
</style>
