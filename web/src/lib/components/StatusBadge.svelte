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

<span
	class={[
		'badge badge-soft font-semibold',
		{ success: 'badge-success', warning: 'badge-warning', danger: 'badge-error', info: 'badge-info' }[tone]
	]}
>
	<span class="status status-current" aria-hidden="true"></span>
	{status}
</span>
