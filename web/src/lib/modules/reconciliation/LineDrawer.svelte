<script lang="ts">
	import { api, ApiError } from '../../api';
	import Drawer from '../../components/Drawer.svelte';
	import Button from '../../components/Button.svelte';
	import Select from '../../components/Select.svelte';
	import Money from '../../components/Money.svelte';
	import DateText from '../../components/DateText.svelte';
	import type { BankLine } from './BankLineTable.svelte';
	import MatchInvoicePanel from './MatchInvoicePanel.svelte';
	import MatchPayoutPanel from './MatchPayoutPanel.svelte';
	import ExpenseForm, {
		type Category,
		type SimpleOption,
		type NewExpense
	} from './ExpenseForm.svelte';

	type Suggestion = {
		kind: string;
		id?: number;
		ref?: string;
		score: number;
		reason: string;
	};

	const IGNORE_REASON_OPTIONS = [
		{ value: 'transfer', label: 'Transfer between own accounts' },
		{ value: 'drawings', label: 'Owner drawings' },
		{ value: 'bond_lodgement', label: 'Bond lodgement' },
		{ value: 'bond_refund', label: 'Bond refund' },
		{ value: 'other', label: 'Other' }
	];

	let {
		open = $bindable(false),
		org,
		line,
		categories,
		suppliers,
		properties,
		projects,
		onReceiptUpload,
		onChanged
	}: {
		open?: boolean;
		org: string;
		line: BankLine | null;
		categories: Category[];
		suppliers: SimpleOption[];
		properties?: SimpleOption[];
		projects?: SimpleOption[];
		onReceiptUpload: (file: File) => Promise<number>;
		// Called after a successful match/ignore/unmatch/expense action, so
		// the bank-lines list can re-fetch; the drawer closes itself.
		onChanged: () => void;
	} = $props();

	let suggestions = $state<Suggestion[]>([]);
	let suggestionsError = $state('');
	let actionError = $state('');
	let actionBusy = $state(false);
	let ignoreReason = $state('transfer');
	let showExpenseForm = $state(false);

	async function loadSuggestions(lineId: number) {
		suggestions = [];
		suggestionsError = '';
		try {
			suggestions = await api<Suggestion[]>(
				'GET',
				`/api/v1/orgs/${org}/bank-lines/${lineId}/suggestions`
			);
		} catch (e) {
			suggestionsError = e instanceof ApiError ? e.message : 'Could not load suggestions.';
		}
	}

	// Re-fetch suggestions (and reset per-open UI state) each time a new
	// unmatched line is opened in the drawer.
	$effect(() => {
		if (!open || !line) return;
		actionError = '';
		showExpenseForm = false;
		ignoreReason = 'transfer';
		if (line.status === 'unmatched') {
			loadSuggestions(line.id);
		} else {
			suggestions = [];
		}
	});

	async function runAction(action: () => Promise<void>) {
		if (actionBusy) return;
		actionBusy = true;
		actionError = '';
		try {
			await action();
			open = false;
			onChanged();
		} catch (e) {
			actionError = e instanceof ApiError ? e.message : 'Something went wrong.';
		} finally {
			actionBusy = false;
		}
	}

	function acceptSuggestion(s: Suggestion) {
		if (!line) return;
		const lineId = line.id;
		if (s.kind === 'invoice' && s.id !== undefined) {
			runAction(async () => {
				await api('POST', `/api/v1/orgs/${org}/bank-lines/${lineId}/match-invoice`, {
					invoice_id: s.id,
					method: 'bank_transfer'
				});
			});
		} else if (s.kind === 'payout' && s.ref !== undefined) {
			runAction(async () => {
				await api('POST', `/api/v1/orgs/${org}/bank-lines/${lineId}/match-payout`, {
					payout_ref: s.ref
				});
			});
		} else if (s.kind === 'expense' && s.id !== undefined) {
			runAction(async () => {
				await api('POST', `/api/v1/orgs/${org}/bank-lines/${lineId}/match-expense`, {
					expense_id: s.id
				});
			});
		}
	}

	function matchInvoice(invoiceId: number) {
		if (!line) return;
		const lineId = line.id;
		runAction(async () => {
			await api('POST', `/api/v1/orgs/${org}/bank-lines/${lineId}/match-invoice`, {
				invoice_id: invoiceId,
				method: 'bank_transfer'
			});
		});
	}

	function matchPayout(payoutRef: string) {
		if (!line) return;
		const lineId = line.id;
		runAction(async () => {
			await api('POST', `/api/v1/orgs/${org}/bank-lines/${lineId}/match-payout`, {
				payout_ref: payoutRef
			});
		});
	}

	function createExpense(expense: NewExpense) {
		if (!line) return;
		const lineId = line.id;
		runAction(async () => {
			await api('POST', `/api/v1/orgs/${org}/bank-lines/${lineId}/expense`, expense);
		});
	}

	function ignoreLine() {
		if (!line) return;
		const lineId = line.id;
		runAction(async () => {
			await api('POST', `/api/v1/orgs/${org}/bank-lines/${lineId}/ignore`, {
				reason: ignoreReason
			});
		});
	}

	function unmatchLine() {
		if (!line) return;
		const lineId = line.id;
		runAction(async () => {
			await api('POST', `/api/v1/orgs/${org}/bank-lines/${lineId}/unmatch`);
		});
	}
</script>

<Drawer bind:open label="Bank line details" side="right" width="28rem">
	{#if line}
		<div class="drawer-content">
			<h2>
				<DateText iso={line.posted_on} /> — {line.description}
			</h2>
			<p class="amount">
				<Money cents={line.amount} />
				{line.amount > 0 ? '(in)' : '(out)'}
			</p>

			{#if line.status === 'unmatched'}
				<section>
					<h3>Suggestions</h3>
					{#if suggestionsError}
						<p class="error" role="alert">{suggestionsError}</p>
					{:else if suggestions.length === 0}
						<p class="muted">No suggestions.</p>
					{:else}
						<ul class="suggestion-list">
							{#each suggestions as s, i (i)}
								<li>
									<span>
										{s.kind} — {s.reason} (score {s.score})
									</span>
									<Button disabled={actionBusy} onclick={() => acceptSuggestion(s)}>
										Accept
									</Button>
								</li>
							{/each}
						</ul>
					{/if}
				</section>

				{#if line.amount > 0}
					<section>
						<h3>Match invoice</h3>
						<MatchInvoicePanel {org} busy={actionBusy} onMatch={matchInvoice} />
					</section>
					<section>
						<h3>Match Stripe payout</h3>
						<MatchPayoutPanel busy={actionBusy} onMatch={matchPayout} />
					</section>
				{:else}
					<section>
						<h3>Create expense</h3>
						{#if !showExpenseForm}
							<Button onclick={() => (showExpenseForm = true)}>Create expense</Button>
						{:else}
							<ExpenseForm
								lineAmountCents={line.amount}
								lineDate={line.posted_on}
								{categories}
								{suppliers}
								{properties}
								{projects}
								busy={actionBusy}
								error={actionError}
								{onReceiptUpload}
								onSubmit={createExpense}
							/>
						{/if}
					</section>
				{/if}

				<section>
					<h3>Ignore</h3>
					<div class="ignore-row">
						<Select id="ignore-reason" bind:value={ignoreReason} options={IGNORE_REASON_OPTIONS} />
						<Button variant="secondary" disabled={actionBusy} onclick={ignoreLine}>
							Ignore line
						</Button>
					</div>
				</section>
			{:else}
				<section>
					<p>
						{line.status === 'ignored' ? 'Ignored' : 'Matched'}
						{#if line.match}: {line.match.label}{/if}
					</p>
					<Button variant="secondary" disabled={actionBusy} onclick={unmatchLine}>Unmatch</Button>
				</section>
			{/if}

			{#if actionError && !showExpenseForm}
				<p class="error" role="alert">{actionError}</p>
			{/if}
		</div>
	{/if}
</Drawer>

<style>
	.drawer-content {
		display: flex;
		flex-direction: column;
		gap: var(--space-4);
	}
	h2 {
		font-size: var(--text-lg);
		margin: 0;
	}
	.amount {
		font-weight: 600;
		font-variant-numeric: tabular-nums;
		margin: 0;
	}
	h3 {
		margin: 0 0 var(--space-2);
		font-size: var(--text-sm);
		text-transform: uppercase;
		color: var(--text-muted);
	}
	.suggestion-list {
		list-style: none;
		margin: 0;
		padding: 0;
		display: flex;
		flex-direction: column;
		gap: var(--space-2);
	}
	.suggestion-list li {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: var(--space-3);
		padding: var(--space-2) var(--space-3);
		border: 1px solid var(--border);
		border-radius: var(--radius-md);
		font-size: var(--text-sm);
	}
	.ignore-row {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-3);
		align-items: center;
	}
	/* Select renders its own <select>, outside this component's scope - a
	   native <select>'s intrinsic width follows its longest option text
	   ("Transfer between own accounts"), which can overflow a narrow
	   drawer; force it to the row's own width instead. */
	.ignore-row :global(select) {
		flex: 1 1 12rem;
		min-width: 0;
	}
	.muted {
		color: var(--text-muted);
	}
	.error {
		color: var(--danger);
		padding: var(--space-2) var(--space-3);
		border-radius: var(--radius-md);
		background: var(--danger-subtle);
	}
</style>
