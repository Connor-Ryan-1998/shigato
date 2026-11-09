<script lang="ts" module>
	// Category is the subset of GET /categories' fields this form needs.
	export type Category = {
		id: number;
		name: string;
		type: string;
	};

	export type SimpleOption = { id: number; name: string };

	// NewExpenseLine is one split line as the create-expense request body
	// wants it (internal/recon/routes.go expenseLineRequest).
	export type NewExpenseLine = {
		category_id: number;
		amount: number;
		gst_code?: string;
		property_id?: number;
		project_id?: number;
	};

	// NewExpense is the body ExpenseForm's onSubmit receives: spent_on is
	// always included (defaulted to the bank line's date), matching both
	// expenseRequest and expenseFromLineRequest's accepted shape.
	export type NewExpense = {
		spent_on: string;
		supplier_contact_id?: number;
		description: string;
		receipt_file_id?: number;
		lines: NewExpenseLine[];
	};
</script>

<script lang="ts">
	import { untrack } from 'svelte';
	import Field from '../../components/Field.svelte';
	import Select from '../../components/Select.svelte';
	import Button from '../../components/Button.svelte';
	import { parseCents, money } from '../../format';

	const GST_OVERRIDE_OPTIONS = [
		{ value: '', label: 'Category default' },
		{ value: 'GST', label: 'GST' },
		{ value: 'FRE', label: 'GST-free' },
		{ value: 'INP', label: 'Input-taxed purchase (INP)' },
		{ value: 'N-T', label: 'Not taxable (N-T)' }
	];

	type Row = {
		id: number;
		categoryId: string;
		amount: string;
		gstCode: string;
		propertyId: string;
		projectId: string;
	};

	let {
		lineAmountCents,
		lineDate,
		categories,
		suppliers = [],
		properties,
		projects,
		busy = false,
		error,
		oncancel,
		onchange,
		onReceiptUpload,
		onSubmit
	}: {
		// When supplied, split lines must sum to the bank debit's magnitude.
		lineAmountCents?: number;
		lineDate: string;
		categories: Category[];
		suppliers?: SimpleOption[];
		// undefined hides the column entirely (the property/timesheets
		// module isn't enabled for this org); an empty array still shows it.
		properties?: SimpleOption[];
		projects?: SimpleOption[];
		busy?: boolean;
		error?: string;
		oncancel?: () => void;
		onchange?: () => void;
		// Uploads the chosen file (POST /files) and resolves to its id. Left
		// undefined in tests that never touch the file input; the parent
		// (which owns org/api access) supplies the real implementation.
		onReceiptUpload?: (file: File) => Promise<number>;
		onSubmit: (expense: NewExpense) => void;
	} = $props();

	let receiptError = $state('');
	let uploadingReceipt = $state(false);

	async function handleReceiptChange(e: Event) {
		const file = (e.currentTarget as HTMLInputElement).files?.[0];
		if (!file || !onReceiptUpload) return;
		receiptError = '';
		// Clear any previously attached receipt up front: if this upload
		// fails, the form must not go on submitting with a stale file id
		// from an earlier, unrelated attachment.
		receiptFileId = undefined;
		uploadingReceipt = true;
		try {
			receiptFileId = await onReceiptUpload(file);
		} catch (err) {
			receiptError = err instanceof Error ? err.message : 'Could not upload the receipt.';
		} finally {
			uploadingReceipt = false;
		}
	}

	// Only "expense"-type categories are valid on a split line
	// (internal/recon/expenses.go rejects any other type); "income"
	// categories exist for the same org but would just bounce off the
	// server, so they're never offered here.
	const expenseCategories = $derived(categories.filter((c) => c.type === 'expense'));

	const uid = $props.id();
	let nextId = 0;
	function emptyRow(): Row {
		return {
			id: nextId++,
			categoryId: expenseCategories[0] ? String(expenseCategories[0].id) : '',
			amount: '',
			gstCode: '',
			propertyId: '',
			projectId: ''
		};
	}

	// Seeded once from lineDate at mount - this component is recreated
	// fresh each time the drawer opens a different line (see LineDrawer),
	// so that's the only time a default is needed; untrack makes the
	// one-shot intent explicit rather than tracking lineDate ongoing.
	let spentOn = $state(untrack(() => lineDate));
	let supplierId = $state('');
	let description = $state('');
	let rows = $state<Row[]>([emptyRow()]);
	let receiptFileId = $state<number | undefined>(undefined);

	const categoryOptions = $derived(
		expenseCategories.map((c) => ({ value: String(c.id), label: c.name }))
	);
	const supplierOptions = $derived([
		{ value: '', label: 'No supplier' },
		...suppliers.map((s) => ({ value: String(s.id), label: s.name }))
	]);
	const propertyOptions = $derived([
		{ value: '', label: 'None' },
		...(properties ?? []).map((p) => ({ value: String(p.id), label: p.name }))
	]);
	const projectOptions = $derived([
		{ value: '', label: 'None' },
		...(projects ?? []).map((p) => ({ value: String(p.id), label: p.name }))
	]);

	const targetCents = $derived(lineAmountCents === undefined ? undefined : Math.abs(lineAmountCents));

	// Integer-cents sum of the parsed split lines - the only client-side
	// money arithmetic in this form, done solely to validate against the
	// bank line's amount before submitting; the server is still the only
	// source of truth for every stored total.
	const parsedAmounts = $derived(rows.map((r) => parseCents(r.amount)));
	const linesTotalCents = $derived(
		parsedAmounts.reduce((sum: number, c) => sum + (c ?? 0), 0)
	);
	const allAmountsValid = $derived(parsedAmounts.every((c) => c !== null && c > 0));
	const totalMismatch = $derived(
		targetCents !== undefined && allAmountsValid && rows.length > 0 && linesTotalCents !== targetCents
	);

	function rowValid(row: Row): boolean {
		if (row.categoryId === '') return false;
		return parseCents(row.amount) !== null && (parseCents(row.amount) ?? 0) > 0;
	}

	const valid = $derived(
		description.trim() !== '' &&
			spentOn !== '' &&
			rows.length > 0 &&
			rows.every(rowValid) &&
			(targetCents === undefined || linesTotalCents === targetCents) &&
			!busy && !uploadingReceipt
	);

	function addRow() {
		if (busy || uploadingReceipt) return;
		rows.push(emptyRow());
		onchange?.();
	}

	function removeRow(id: number) {
		if (busy || uploadingReceipt) return;
		rows = rows.filter((r) => r.id !== id);
		onchange?.();
	}

	function submit() {
		if (!valid) return;
		const lines: NewExpenseLine[] = rows.map((r) => {
			const line: NewExpenseLine = {
				category_id: Number(r.categoryId),
				amount: parseCents(r.amount)!
			};
			if (r.gstCode !== '') line.gst_code = r.gstCode;
			if (r.propertyId !== '') line.property_id = Number(r.propertyId);
			if (r.projectId !== '') line.project_id = Number(r.projectId);
			return line;
		});
		const expense: NewExpense = {
			spent_on: spentOn,
			description,
			lines
		};
		if (supplierId !== '') expense.supplier_contact_id = Number(supplierId);
		if (receiptFileId !== undefined) expense.receipt_file_id = receiptFileId;
		onSubmit(expense);
	}
</script>

<form onsubmit={(event) => { event.preventDefault(); submit(); }} oninput={() => onchange?.()} onchange={() => onchange?.()}>
<fieldset class="m-0 flex min-w-0 flex-col gap-4 border-0 p-0" disabled={busy || uploadingReceipt}>
	<legend class="mb-2 font-semibold">Expense details</legend>
	<div class="flex flex-col gap-3">
		<Field label="Spent on" id={`expense-${uid}-spent-on`}>
			<input class="input w-full" id={`expense-${uid}-spent-on`} type="date" bind:value={spentOn} />
		</Field>
		<Field label="Supplier" id={`expense-${uid}-supplier`}>
			<Select id={`expense-${uid}-supplier`} bind:value={supplierId} options={supplierOptions} />
		</Field>
		<Field label="Description" id={`expense-${uid}-description`}>
			<input class="input w-full" id={`expense-${uid}-description`} bind:value={description} />
		</Field>
	</div>

	<h3 class="m-0 mt-2 font-bold">Lines</h3>
	{#each rows as row, i (row.id)}
		<div
			class="grid grid-cols-1 items-end gap-3 border-b border-base-300 pb-3 sm:grid-cols-[repeat(auto-fit,minmax(8rem,1fr))] [&_select]:w-full [&_select]:min-w-0"
		>
			<Field label="Category" id={`expense-${uid}-${row.id}-category`}>
				<Select
					id={`expense-${uid}-${row.id}-category`}
					bind:value={row.categoryId}
					options={categoryOptions}
				/>
			</Field>
			<Field label="Amount ($)" id={`expense-${uid}-${row.id}-amount`}>
				<input
					class="input w-full"
					id={`expense-${uid}-${row.id}-amount`}
					inputmode="decimal"
					bind:value={row.amount}
				/>
			</Field>
			<Field label="GST" id={`expense-${uid}-${row.id}-gst`}>
				<Select
					id={`expense-${uid}-${row.id}-gst`}
					bind:value={row.gstCode}
					options={GST_OVERRIDE_OPTIONS}
				/>
			</Field>
			{#if properties !== undefined}
				<Field label="Property" id={`expense-${uid}-${row.id}-property`}>
					<Select
						id={`expense-${uid}-${row.id}-property`}
						bind:value={row.propertyId}
						options={propertyOptions}
					/>
				</Field>
			{/if}
			{#if projects !== undefined}
				<Field label="Project" id={`expense-${uid}-${row.id}-project`}>
					<Select
						id={`expense-${uid}-${row.id}-project`}
						bind:value={row.projectId}
						options={projectOptions}
					/>
				</Field>
			{/if}
			<Button variant="secondary" onclick={() => removeRow(row.id)}>
				Remove<span class="sr-only"> line {i + 1}</span>
			</Button>
		</div>
	{/each}

	<div class="flex justify-end gap-3">
		<Button variant="secondary" onclick={addRow}>Add line</Button>
	</div>

	<p class="font-semibold tabular-nums" class:text-error={totalMismatch}>
		Lines total {money(linesTotalCents)}
		{#if totalMismatch}
			but the bank line is {money(targetCents!)}
		{/if}
	</p>

	<Field
		label="Receipt"
		id={`expense-${uid}-receipt`}
		hint="PDF, JPEG or PNG."
		error={receiptError || undefined}
	>
		<input
			class="file-input w-full"
			id={`expense-${uid}-receipt`}
			type="file"
			accept="application/pdf,image/jpeg,image/png"
			disabled={uploadingReceipt}
			onchange={handleReceiptChange}
		/>
	</Field>
	{#if uploadingReceipt}
		<p class="m-0 text-sm text-base-content/70">Uploading…</p>
	{:else if receiptFileId !== undefined}
		<p class="m-0 text-sm text-base-content/70">Receipt attached.</p>
	{/if}

	{#if error}
		<p class="alert alert-error alert-soft" role="alert">{error}</p>
	{/if}

	<div class="flex justify-end gap-3">
		{#if oncancel}<Button variant="secondary" onclick={oncancel}>Cancel</Button>{/if}
		<Button type="submit" {busy} disabled={!valid || uploadingReceipt}>Save</Button>
	</div>
</fieldset>
</form>
