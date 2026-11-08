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
	class="select"
	bind:value
	{disabled}
	onchange={() => onchange?.(value)}
>
	{#each options as opt (opt.value)}
		<option value={opt.value}>{opt.label}</option>
	{/each}
</select>
