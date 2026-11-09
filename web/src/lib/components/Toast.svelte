<script lang="ts">
	let {
		message,
		tone = 'info',
		onDismiss
	}: {
		message: string;
		tone?: 'success' | 'warning' | 'danger' | 'info';
		onDismiss?: () => void;
	} = $props();
</script>

<div
	class={[
		'alert alert-soft',
		{ success: 'alert-success', warning: 'alert-warning', danger: 'alert-error', info: 'alert-info' }[tone]
	]}
	role={tone === 'danger' ? 'alert' : 'status'}
>
	<span>{message}</span>
	{#if onDismiss}
		<button type="button" class="btn btn-ghost btn-sm btn-circle" aria-label="Dismiss" onclick={onDismiss}>&times;</button>
	{/if}
</div>
