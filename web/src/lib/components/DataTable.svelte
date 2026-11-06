<script lang="ts" generics="T">
	import type { Snippet } from 'svelte';

	type Column = {
		key: string;
		label: string;
		cell?: Snippet<[T]>;
	};

	let {
		columns,
		rows,
		rowKey
	}: {
		columns: Column[];
		rows: T[];
		rowKey: (row: T) => string | number;
	} = $props();
</script>

<table class="data-table">
	<thead>
		<tr>
			{#each columns as col (col.key)}
				<th scope="col">{col.label}</th>
			{/each}
		</tr>
	</thead>
	<tbody>
		{#each rows as row (rowKey(row))}
			<tr>
				{#each columns as col (col.key)}
					<td data-label={col.label}>
						{#if col.cell}
							{@render col.cell(row)}
						{:else}
							{(row as Record<string, unknown>)[col.key]}
						{/if}
					</td>
				{/each}
			</tr>
		{/each}
	</tbody>
</table>

<style>
	.data-table {
		width: 100%;
		border-collapse: collapse;
	}
	th,
	td {
		text-align: left;
		padding: var(--space-2) var(--space-3);
		border-bottom: 1px solid var(--border);
	}
	th {
		color: var(--text-muted);
		font-size: var(--text-sm);
		font-weight: 600;
	}

	/* Below 640px a row becomes a card: each cell stacks with its column
	   label in front of it, rather than scrolling a cramped table
	   sideways. */
	@media (max-width: 640px) {
		thead {
			display: none;
		}
		.data-table,
		tbody,
		tr,
		td {
			display: block;
			width: 100%;
		}
		tr {
			border: 1px solid var(--border);
			border-radius: var(--radius-md);
			margin-bottom: var(--space-3);
			padding: var(--space-2);
		}
		td {
			display: flex;
			justify-content: space-between;
			gap: var(--space-3);
			border-bottom: none;
		}
		td::before {
			content: attr(data-label);
			font-weight: 600;
			color: var(--text-muted);
		}
	}
</style>
