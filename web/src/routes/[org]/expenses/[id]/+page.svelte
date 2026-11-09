<script lang="ts">
	import { page } from '$app/state';
	import { api, ApiError } from '../../../../lib/api';
	import { session } from '../../../../lib/session.svelte';
	import { money } from '../../../../lib/format';
	import PageHeader from '../../../../lib/components/PageHeader.svelte';
	import Button from '../../../../lib/components/Button.svelte';
	import DataTable from '../../../../lib/components/DataTable.svelte';
	import type { Expense, ExpenseLine } from '../../../../lib/modules/reconciliation/expenses';
	import type { SimpleOption } from '../../../../lib/modules/reconciliation/ExpenseForm.svelte';

	const org = $derived(page.params.org);
	const id = $derived(page.params.id);
	const key = $derived(`${org}:${id}`);
	const membership = $derived(session.currentOrg?.slug === org ? session.currentOrg : null);
	const canRead = $derived(session.modules.includes('reconciliation') && (membership?.perms.includes('reports_read') ?? false));
	let expense = $state<Expense | null>(null);
	let loadedKey = $state('');
	let error = $state('');
	let seq = 0;
	let lookupSeq = 0;
	let categories = $state<SimpleOption[]>([]);
	let suppliers = $state<SimpleOption[]>([]);
	let properties = $state<SimpleOption[]>([]);
	let projects = $state<SimpleOption[]>([]);
	let lookupWarning = $state('');

	async function load(slug: string, expenseID: string) {
		const request = ++seq;
		const target = `${slug}:${expenseID}`;
		expense = null;
		loadedKey = '';
		error = '';
		try {
			const result = await api<Expense>('GET', `/api/v1/orgs/${slug}/expenses/${expenseID}`);
			if (request !== seq || target !== key || !canRead) return;
			expense = result;
		} catch (e) {
			if (request !== seq || target !== key || !canRead) return;
			error = e instanceof ApiError ? e.message : 'Could not load expense.';
		} finally {
			if (request === seq && target === key && canRead) loadedKey = target;
		}
	}

	async function loadLookups(slug: string) {
		const request = ++lookupSeq;
		categories = []; suppliers = []; properties = []; projects = [];
		lookupWarning = '';
		const paths = [
			'categories', membership?.perms.includes('invoices_read') ? 'contacts?kind=supplier' : '',
			session.modules.includes('property') ? 'properties' : '',
			session.modules.includes('timesheets') ? 'projects' : ''
		];
		const results = await Promise.allSettled(paths.map((path) => path ? api<SimpleOption[]>('GET', `/api/v1/orgs/${slug}/${path}`) : Promise.resolve([])));
		if (request !== lookupSeq || slug !== org || !canRead) return;
		[categories, suppliers, properties, projects] = results.map((result) => result.status === 'fulfilled' ? result.value : []);
		if (results.some((result) => result.status === 'rejected')) lookupWarning = 'Some names could not be loaded; stored IDs are shown instead.';
	}
	$effect(() => {
		if (canRead && org && id) load(org, id);
		return () => { seq++; };
	});
	$effect(() => {
		if (canRead && org) loadLookups(org);
		return () => { lookupSeq++; };
	});
	function label(options: SimpleOption[], value: number | undefined, kind: string) {
		return value === undefined ? '—' : options.find((option) => option.id === value)?.name ?? `${kind} #${value}`;
	}
</script>

<PageHeader title={`Expense ${id}`} back={{ href: `/${org}/expenses`, label: 'Back to expenses' }} />
{#snippet categoryCell(line: ExpenseLine)}{label(categories, line.category_id, 'Category')}{/snippet}
{#snippet amountCell(line: ExpenseLine)}{money(line.amount)}{/snippet}
{#snippet gstCell(line: ExpenseLine)}{money(line.gst_amount)}{/snippet}
{#snippet propertyCell(line: ExpenseLine)}{label(properties, line.property_id, 'Property')}{/snippet}
{#snippet projectCell(line: ExpenseLine)}{label(projects, line.project_id, 'Project')}{/snippet}

{#if !canRead}
	<p role="alert">Expenses are unavailable for this organisation.</p>
{:else if loadedKey !== key}
	<p role="status">Loading expense…</p>
{:else if error}
	<p role="alert" class="alert alert-error alert-soft mb-2">{error}</p>
	<Button onclick={() => org && id && load(org, id)}>Retry expense</Button>
{:else if expense}
	<h2 class="mb-2 text-lg font-semibold">{expense.description}</h2>
	<dl class="mb-4 grid grid-cols-[max-content_1fr] gap-x-4 gap-y-1 [&_dt]:font-semibold [&_dt]:text-base-content/70">
		<dt>Spent on</dt><dd>{expense.spent_on}</dd>
		<dt>Supplier</dt><dd>{expense.supplier_contact_id ? label(suppliers, expense.supplier_contact_id, 'Supplier') : 'No supplier'}</dd>
		<dt>Total</dt><dd>{money(expense.total)}</dd>
		<dt>Bank match</dt><dd>{expense.bank_line_id ? `Matched to bank line ${expense.bank_line_id}` : 'Not matched to bank'}</dd>
	</dl>
	{#if lookupWarning}<p role="status" class="alert alert-warning alert-soft mb-4">{lookupWarning}</p>{/if}
	<h2 class="mb-2 text-lg font-semibold">Category splits</h2>
	<DataTable columns={[
		{ key: 'category', label: 'Category', cell: categoryCell }, { key: 'amount', label: 'Amount', cell: amountCell },
		{ key: 'gst_code', label: 'GST code' }, { key: 'gst_amount', label: 'GST amount', cell: gstCell },
		{ key: 'property', label: 'Property', cell: propertyCell }, { key: 'project', label: 'Project', cell: projectCell }
	]} rows={expense.lines} rowKey={(line) => line.id} />
	{#if expense.receipt_file_id}
		<p class="mt-4"><a class="link link-primary" href={`/api/v1/orgs/${org}/files/${expense.receipt_file_id}`} download>Download receipt</a></p>
	{:else}
		<p class="mt-4 text-base-content/70">No receipt attached.</p>
	{/if}
{/if}
