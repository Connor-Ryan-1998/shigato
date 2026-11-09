<script lang="ts" generics="T">
	import type { Snippet } from 'svelte';

	type Column = {
		key: string;
		label: string;
		cell?: Snippet<[T]>;
		align?: 'left' | 'right';
	};

	let {
		columns,
		rows,
		caption,
		rowKey
	}: {
		columns: Column[];
		rows: T[];
		caption?: string;
		rowKey: (row: T) => string | number;
	} = $props();
</script>

<div class="overflow-x-auto rounded-box border border-base-300 bg-base-100">
<table class="table">
	{#if caption}<caption class="sr-only">{caption}</caption>{/if}
	<thead>
		<tr>
			{#each columns as col (col.key)}
				<th scope="col" class:text-right={col.align === 'right'}>{col.label}</th>
			{/each}
		</tr>
	</thead>
	<tbody>
		{#each rows as row (rowKey(row))}
			<tr>
				{#each columns as col (col.key)}
					<td data-label={col.label} class:text-right={col.align === 'right'} class:tabular-nums={col.align === 'right'}>
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
