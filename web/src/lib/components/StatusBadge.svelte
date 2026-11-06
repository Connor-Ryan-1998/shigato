<script lang="ts">
	// Status is always carried by the text label, never by the dot's colour
	// alone - a colour-blind reader, or a screen reader, still gets it.
	let {
		status,
		tone = 'info'
	}: {
		status: string;
		tone?: 'success' | 'warning' | 'danger' | 'info';
	} = $props();
</script>

<span class="badge {tone}">
	<span class="dot" aria-hidden="true"></span>
	{status}
</span>

<style>
	.badge {
		display: inline-flex;
		align-items: center;
		gap: var(--space-1);
		padding: var(--space-1) var(--space-2);
		border-radius: var(--radius-full);
		font-size: var(--text-sm);
		font-weight: 600;
	}
	.dot {
		width: 0.5em;
		height: 0.5em;
		border-radius: 50%;
	}
	.success {
		background: var(--success-subtle);
		color: var(--success);
	}
	.success .dot {
		background: var(--success);
	}
	.warning {
		background: var(--warning-subtle);
		color: var(--warning);
	}
	.warning .dot {
		background: var(--warning);
	}
	.danger {
		background: var(--danger-subtle);
		color: var(--danger);
	}
	.danger .dot {
		background: var(--danger);
	}
	.info {
		background: var(--info-subtle);
		color: var(--info);
	}
	.info .dot {
		background: var(--info);
	}
</style>
