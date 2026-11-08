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

<div class="overflow-x-auto">
<table class="table">
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
</div>
