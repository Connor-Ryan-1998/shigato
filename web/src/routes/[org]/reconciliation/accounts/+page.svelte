<script lang="ts">
	import { onMount } from 'svelte';
	import { page } from '$app/state';
	import { api, ApiError } from '../../../../lib/api';
	import PageHeader from '../../../../lib/components/PageHeader.svelte';
	import Button from '../../../../lib/components/Button.svelte';
	import Field from '../../../../lib/components/Field.svelte';
	import Select from '../../../../lib/components/Select.svelte';
	import Dialog from '../../../../lib/components/Dialog.svelte';
	import EmptyState from '../../../../lib/components/EmptyState.svelte';
	import ImportDialog from '../../../../lib/modules/reconciliation/ImportDialog.svelte';

	type CSVMapping = {
		has_header: boolean;
		date_column: number;
		date_format: string;
		amount_column?: number;
		debit_column?: number;
		credit_column?: number;
		description_column: number;
	};

	type BankAccount = {
		id: number;
		name: string;
		csv_mapping: CSVMapping;
	};

	const DATE_FORMAT_OPTIONS = [
		{ value: '02/01/2006', label: 'DD/MM/YYYY' },
		{ value: '2006-01-02', label: 'YYYY-MM-DD' },
		{ value: '2 Jan 2006', label: 'D Mon YYYY' }
	];

	const MAPPING_KIND_OPTIONS = [
		{ value: 'preset', label: 'CommBank export' },
		{ value: 'custom', label: 'Custom mapping' }
	];

	let accounts = $state<BankAccount[]>([]);
	let loadStatus = $state<'loading' | 'error' | 'ready'>('loading');
	let loadError = $state('');

	async function load() {
		loadStatus = 'loading';
		try {
			const org = page.params.org;
			accounts = await api<BankAccount[]>('GET', `/api/v1/orgs/${org}/bank-accounts`);
			loadStatus = 'ready';
		} catch (e) {
			loadError = e instanceof ApiError ? e.message : 'Something went wrong.';
			loadStatus = 'error';
		}
	}

	onMount(load);

	// --- add account dialog ---
	let addOpen = $state(false);
	let addError = $state('');
	let addBusy = $state(false);
	let newName = $state('');
	let mappingKind = $state('preset');
	let hasHeader = $state(true);
	let dateColumn = $state('0');
	let dateFormat = $state('02/01/2006');
	let descriptionColumn = $state('2');
	let amountStyle = $state<'single' | 'debit_credit'>('single');
	let amountColumn = $state('1');
	let debitColumn = $state('1');
	let creditColumn = $state('2');

	function openAddDialog() {
		addError = '';
		newName = '';
		mappingKind = 'preset';
		hasHeader = true;
		dateColumn = '0';
		dateFormat = '02/01/2006';
		descriptionColumn = '2';
		amountStyle = 'single';
		amountColumn = '1';
		debitColumn = '1';
		creditColumn = '2';
		addOpen = true;
	}

	function parseColumn(v: string): number | null {
		if (!/^\d+$/.test(v.trim())) return null;
		return Number(v);
	}

	async function createAccount() {
		if (addBusy) return;
		if (newName.trim() === '') {
			addError = 'Name is required.';
			return;
		}
		addBusy = true;
		addError = '';
		try {
			const org = page.params.org;
			const body: { name: string; preset?: string; csv_mapping?: CSVMapping } = {
				name: newName
			};
			if (mappingKind === 'preset') {
				body.preset = 'commbank';
			} else {
				const dateCol = parseColumn(dateColumn);
				const descCol = parseColumn(descriptionColumn);
				if (dateCol === null || descCol === null) {
					addError = 'Columns must be whole numbers.';
					addBusy = false;
					return;
				}
				const mapping: CSVMapping = {
					has_header: hasHeader,
					date_column: dateCol,
					date_format: dateFormat,
					description_column: descCol
				};
				if (amountStyle === 'single') {
					const col = parseColumn(amountColumn);
					if (col === null) {
						addError = 'Columns must be whole numbers.';
						addBusy = false;
						return;
					}
					mapping.amount_column = col;
				} else {
					const debit = parseColumn(debitColumn);
					const credit = parseColumn(creditColumn);
					if (debit === null || credit === null) {
						addError = 'Columns must be whole numbers.';
						addBusy = false;
						return;
					}
					mapping.debit_column = debit;
					mapping.credit_column = credit;
				}
				body.csv_mapping = mapping;
			}
			await api('POST', `/api/v1/orgs/${org}/bank-accounts`, body);
			addOpen = false;
			await load();
		} catch (e) {
			addError = e instanceof ApiError ? e.message : 'Something went wrong.';
		} finally {
			addBusy = false;
		}
	}

	// --- import dialog ---
	let importOpen = $state(false);
	let importAccount = $state<BankAccount | null>(null);

	function openImport(account: BankAccount) {
		importAccount = account;
		importOpen = true;
	}
</script>

<PageHeader title="Bank accounts">
	{#snippet actions()}
		<Button onclick={openAddDialog}>Add account</Button>
	{/snippet}
</PageHeader>

{#if loadStatus === 'loading'}
	<p aria-live="polite">Loading accounts…</p>
{:else if loadStatus === 'error'}
	<EmptyState title="Couldn't load bank accounts" description={loadError}>
		{#snippet action()}
			<Button onclick={load}>Retry</Button>
		{/snippet}
	</EmptyState>
{:else if accounts.length === 0}
	<EmptyState title="No bank accounts yet" description="Add one to start importing statements." />
{:else}
	<ul class="account-list">
		{#each accounts as account (account.id)}
			<li>
				<span class="account-name">{account.name}</span>
				<Button variant="secondary" onclick={() => openImport(account)}>Import CSV</Button>
			</li>
		{/each}
	</ul>
{/if}

<Dialog bind:open={addOpen} title="Add bank account">
	<div class="fields">
		<Field label="Name" id="account-name">
			<input id="account-name" bind:value={newName} />
		</Field>
		<Field label="CSV format" id="mapping-kind">
			<Select id="mapping-kind" bind:value={mappingKind} options={MAPPING_KIND_OPTIONS} />
		</Field>

		{#if mappingKind === 'custom'}
			<Field label="Has header row?" id="has-header">
				<input id="has-header" type="checkbox" bind:checked={hasHeader} />
			</Field>
			<Field label="Date column (0-based)" id="date-column">
				<input id="date-column" inputmode="numeric" bind:value={dateColumn} />
			</Field>
			<Field label="Date format" id="date-format">
				<Select id="date-format" bind:value={dateFormat} options={DATE_FORMAT_OPTIONS} />
			</Field>
			<Field label="Description column (0-based)" id="description-column">
				<input id="description-column" inputmode="numeric" bind:value={descriptionColumn} />
			</Field>
			<Field label="Amount style" id="amount-style">
				<Select
					id="amount-style"
					bind:value={amountStyle}
					options={[
						{ value: 'single', label: 'Single signed column' },
						{ value: 'debit_credit', label: 'Separate debit/credit columns' }
					]}
				/>
			</Field>
			{#if amountStyle === 'single'}
				<Field label="Amount column (0-based)" id="amount-column">
					<input id="amount-column" inputmode="numeric" bind:value={amountColumn} />
				</Field>
			{:else}
				<Field label="Debit column (0-based)" id="debit-column">
					<input id="debit-column" inputmode="numeric" bind:value={debitColumn} />
				</Field>
				<Field label="Credit column (0-based)" id="credit-column">
					<input id="credit-column" inputmode="numeric" bind:value={creditColumn} />
				</Field>
			{/if}
		{/if}
	</div>
	{#if addError}
		<p class="dialog-error" role="alert">{addError}</p>
	{/if}
	<div class="dialog-actions">
		<Button variant="secondary" disabled={addBusy} onclick={() => (addOpen = false)}>Cancel</Button>
		<Button disabled={addBusy} onclick={createAccount}>Create</Button>
	</div>
</Dialog>

{#if importAccount}
	<ImportDialog
		bind:open={importOpen}
		org={page.params.org ?? ''}
		accountId={importAccount.id}
		accountName={importAccount.name}
	/>
{/if}

<style>
	.account-list {
		list-style: none;
		margin: 0;
		padding: 0;
		display: flex;
		flex-direction: column;
		gap: var(--space-3);
	}
	.account-list li {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: var(--space-3);
		padding: var(--space-3) var(--space-4);
		border: 1px solid var(--border);
		border-radius: var(--radius-md);
	}
	.account-name {
		font-weight: 600;
	}
	.fields {
		display: flex;
		flex-direction: column;
		gap: var(--space-3);
		margin-bottom: var(--space-4);
	}
	.dialog-actions {
		display: flex;
		justify-content: flex-end;
		gap: var(--space-3);
	}
	.dialog-error {
		margin: 0 0 var(--space-4);
		padding: var(--space-2) var(--space-3);
		border-radius: var(--radius-md);
		background: var(--danger-subtle);
		color: var(--danger);
	}
</style>
