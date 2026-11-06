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

<div class="toast {tone}" role="status">
	<span>{message}</span>
	{#if onDismiss}
		<button type="button" aria-label="Dismiss" onclick={onDismiss}>&times;</button>
	{/if}
</div>

<style>
	.toast {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: var(--space-3);
		padding: var(--space-3) var(--space-4);
		border-radius: var(--radius-md);
		box-shadow: var(--shadow-md);
	}
	.success {
		background: var(--success-subtle);
		color: var(--success);
	}
	.warning {
		background: var(--warning-subtle);
		color: var(--warning);
	}
	.danger {
		background: var(--danger-subtle);
		color: var(--danger);
	}
	.info {
		background: var(--info-subtle);
		color: var(--info);
	}
	button {
		background: none;
		border: none;
		font-size: 1.25rem;
		line-height: 1;
		cursor: pointer;
		color: inherit;
	}
</style>
