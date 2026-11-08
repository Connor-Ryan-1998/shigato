<script lang="ts" module>
	// ManualLine is one line of a manual invoice as the create-invoice request
	// body wants it (POST /invoices' lines[]): already parsed from the
	// editable row's raw text into quantity_milli/unit_amount integers.
	export type ManualLine = {
		description: string;
		quantity_milli: number;
		unit_amount: number;
		gst_code: string;
	};
</script>

<script lang="ts">
	import Field from '../../../components/Field.svelte';
	import Select from '../../../components/Select.svelte';
	import Button from '../../../components/Button.svelte';
	import { parseCents, parseQuantityMilli } from '../../../format';

	// INP (an imported/synced line) is deliberately not offered here - this
	// editor only ever produces manual invoices.
	const GST_OPTIONS = [
		{ value: 'GST', label: 'GST' },
		{ value: 'FRE', label: 'GST-free' },
		{ value: 'N-T', label: 'Not taxable (N-T)' }
	];

	type Row = {
		id: number;
		description: string;
		quantity: string;
		unitAmount: string;
		gstCode: string;
	};

	let { onSave }: { onSave: (lines: ManualLine[]) => void } = $props();

	const uid = $props.id();
	let nextId = 0;
	function emptyRow(): Row {
		return { id: nextId++, description: '', quantity: '', unitAmount: '', gstCode: 'GST' };
	}

	let rows = $state<Row[]>([emptyRow()]);

	// Blank fields don't show an error yet - a freshly-added row shouldn't
	// nag before the user has typed anything - but they still block Save
	// (rowValid), since an incomplete line can't be submitted.
	function quantityError(row: Row): string | undefined {
		if (row.quantity.trim() === '') return undefined;
		const parsed = parseQuantityMilli(row.quantity);
		if (parsed === null) return 'Enter a valid quantity, up to 3 decimal places.';
		if (parsed <= 0) return 'Quantity must be greater than zero.';
		return undefined;
	}

	function amountError(row: Row): string | undefined {
		if (row.unitAmount.trim() === '') return undefined;
		if (parseCents(row.unitAmount) === null) return 'Enter a valid amount, up to 2 decimal places.';
		return undefined;
	}

	function rowValid(row: Row): boolean {
		if (row.description.trim() === '') return false;
		const quantity = parseQuantityMilli(row.quantity);
		if (quantity === null || quantity <= 0) return false;
		return parseCents(row.unitAmount) !== null;
	}

	const valid = $derived(rows.length > 0 && rows.every(rowValid));

	function addRow() {
		rows.push(emptyRow());
	}

	function removeRow(id: number) {
		rows = rows.filter((r) => r.id !== id);
	}

	function save() {
		if (!valid) return;
		const lines: ManualLine[] = rows.map((r) => ({
			description: r.description,
			quantity_milli: parseQuantityMilli(r.quantity)!,
			unit_amount: parseCents(r.unitAmount)!,
			gst_code: r.gstCode
		}));
		onSave(lines);
	}
</script>

<div class="flex flex-col gap-4">
	{#each rows as row, i (row.id)}
		<div
			class="grid grid-cols-1 items-end gap-3 border-b border-base-300 pb-3 sm:grid-cols-[minmax(0,2fr)_minmax(0,1fr)_minmax(0,1fr)_minmax(0,1fr)_auto] [&_select]:w-full [&_select]:min-w-0"
		>
			<Field label="Description" id={`line-${uid}-${row.id}-description`}>
				<input class="input w-full" id={`line-${uid}-${row.id}-description`} bind:value={row.description} />
			</Field>
			<Field
				label="Quantity"
				id={`line-${uid}-${row.id}-quantity`}
				error={quantityError(row)}
			>
				<input
					class="input w-full"
					id={`line-${uid}-${row.id}-quantity`}
					inputmode="decimal"
					bind:value={row.quantity}
				/>
			</Field>
			<Field
				label="Unit price ($)"
				id={`line-${uid}-${row.id}-amount`}
				error={amountError(row)}
			>
				<input
					class="input w-full"
					id={`line-${uid}-${row.id}-amount`}
					inputmode="decimal"
					bind:value={row.unitAmount}
				/>
			</Field>
			<Field label="GST code" id={`line-${uid}-${row.id}-gst`}>
				<Select id={`line-${uid}-${row.id}-gst`} bind:value={row.gstCode} options={GST_OPTIONS} />
			</Field>
			<Button variant="secondary" onclick={() => removeRow(row.id)}>
				Remove<span class="sr-only"> line {i + 1}</span>
			</Button>
		</div>
	{/each}

	<div class="flex gap-3">
		<Button variant="secondary" onclick={addRow}>Add line</Button>
		<Button onclick={save} disabled={!valid}>Save</Button>
	</div>
</div>
