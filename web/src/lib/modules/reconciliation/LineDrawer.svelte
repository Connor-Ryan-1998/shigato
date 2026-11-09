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
	let actionBusy = $state(false);
	// Each action kind gets its own error slot, so (e.g.) a failed Ignore
	// renders next to the Ignore controls rather than inside whatever
	// unrelated section (the expense form, the match panels) happened to
	// own the one shared error string before.
	let matchError = $state('');
	let expenseError = $state('');
	let ignoreError = $state('');
	let unmatchError = $state('');
	let expenseWarnings = $state<string[]>([]);
	let ignoreReason = $state('transfer');
	let showExpenseForm = $state(false);

	async function loadSuggestions(lineId: number) {
		suggestions = [];
		suggestionsError = '';
		try {
			const result = await api<Suggestion[]>(
				'GET',
				`/api/v1/orgs/${org}/bank-lines/${lineId}/suggestions`
			);
			// The drawer may have moved on to a different (or no) line while
			// this request was in flight - applying a stale response here
			// would show line B's suggestions as if they were line A's, and
			// Accept would then post a match for the wrong line entirely.
			if (line?.id !== lineId) return;
			suggestions = result;
		} catch (e) {
			if (line?.id !== lineId) return;
			suggestionsError = e instanceof ApiError ? e.message : 'Could not load suggestions.';
		}
	}

	// Re-fetch suggestions (and reset per-open UI state) each time a new
	// unmatched line is opened in the drawer.
	$effect(() => {
		if (!open || !line) return;
		matchError = '';
		expenseError = '';
		ignoreError = '';
		unmatchError = '';
		expenseWarnings = [];
		showExpenseForm = false;
		ignoreReason = 'transfer';
		if (line.status === 'unmatched') {
			loadSuggestions(line.id);
		} else {
			suggestions = [];
		}
	});

	// runAction is shared by every action that closes the drawer on success
	// (everything except createExpense, which may need to stay open to show
	// warnings). setError lets each caller route a failure to its own error
	// slot instead of a single shared one that the wrong section would show.
	async function runAction(action: () => Promise<void>, setError: (msg: string) => void) {
		if (actionBusy) return;
		actionBusy = true;
		setError('');
		try {
			await action();
			onChanged();
			open = false;
		} catch (e) {
			setError(e instanceof ApiError ? e.message : 'Something went wrong.');
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
			}, (m) => (matchError = m));
		} else if (s.kind === 'payout' && s.ref !== undefined) {
			runAction(async () => {
				await api('POST', `/api/v1/orgs/${org}/bank-lines/${lineId}/match-payout`, {
					payout_ref: s.ref
				});
			}, (m) => (matchError = m));
		} else if (s.kind === 'expense' && s.id !== undefined) {
			runAction(async () => {
				await api('POST', `/api/v1/orgs/${org}/bank-lines/${lineId}/match-expense`, {
					expense_id: s.id
				});
			}, (m) => (matchError = m));
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
		}, (m) => (matchError = m));
	}

	function matchPayout(payoutRef: string) {
		if (!line) return;
		const lineId = line.id;
		runAction(async () => {
			await api('POST', `/api/v1/orgs/${org}/bank-lines/${lineId}/match-payout`, {
				payout_ref: payoutRef
			});
		}, (m) => (matchError = m));
	}

	// createExpense doesn't go through runAction: on success the server may
	// return warnings (e.g. GST computed oddly for a line), which must stay
	// visible to the user - unlike every other action here, the drawer
	// can't just close itself and rely on a Toast (the list already
	// refreshes either way, since the expense was created either way).
	async function createExpense(expense: NewExpense) {
		if (!line || actionBusy) return;
		const lineId = line.id;
		actionBusy = true;
		expenseError = '';
		expenseWarnings = [];
		try {
			const result = await api<{ warnings: string[] }>(
				'POST',
				`/api/v1/orgs/${org}/bank-lines/${lineId}/expense`,
				expense
			);
			onChanged();
			if (result.warnings.length > 0) {
				expenseWarnings = result.warnings;
				showExpenseForm = false;
			} else {
				open = false;
			}
		} catch (e) {
			expenseError = e instanceof ApiError ? e.message : 'Something went wrong.';
		} finally {
			actionBusy = false;
		}
	}

	function ignoreLine() {
		if (!line) return;
		const lineId = line.id;
		runAction(async () => {
			await api('POST', `/api/v1/orgs/${org}/bank-lines/${lineId}/ignore`, {
				reason: ignoreReason
			});
		}, (m) => (ignoreError = m));
	}

	function unmatchLine() {
		if (!line) return;
		const lineId = line.id;
		runAction(async () => {
			await api('POST', `/api/v1/orgs/${org}/bank-lines/${lineId}/unmatch`);
		}, (m) => (unmatchError = m));
	}
</script>

<Drawer bind:open label="Bank line details" side="right" width="28rem" busy={actionBusy}>
	{#if line}
		<div class="flex flex-col gap-4">
			<h2 class="m-0 text-lg font-bold">
				<DateText iso={line.posted_on} /> — {line.description}
			</h2>
			<p class="m-0 font-semibold tabular-nums">
				<Money cents={line.amount} />
				{line.amount > 0 ? '(in)' : '(out)'}
			</p>

			{#if line.status === 'unmatched'}
				<section>
					<h3 class="m-0 mb-2 text-sm text-base-content/70 uppercase">Suggestions</h3>
					{#if suggestionsError}
						<p class="alert alert-error alert-soft" role="alert">{suggestionsError}</p>
					{:else if suggestions.length === 0}
						<p class="text-base-content/70">No suggestions.</p>
					{:else}
						<ul class="m-0 flex list-none flex-col gap-2 p-0">
							{#each suggestions as s, i (i)}
								<li class="flex items-center justify-between gap-3 rounded-box border border-base-300 px-3 py-2 text-sm">
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
					{#if matchError}
						<p class="alert alert-error alert-soft" role="alert">{matchError}</p>
					{/if}
				</section>

				{#if line.amount > 0}
					<section>
						<h3 class="m-0 mb-2 text-sm text-base-content/70 uppercase">Match invoice</h3>
						<MatchInvoicePanel {org} busy={actionBusy} onMatch={matchInvoice} />
					</section>
					<section>
						<h3 class="m-0 mb-2 text-sm text-base-content/70 uppercase">Match Stripe payout</h3>
						<MatchPayoutPanel busy={actionBusy} onMatch={matchPayout} />
					</section>
				{:else}
					<section>
						<h3 class="m-0 mb-2 text-sm text-base-content/70 uppercase">Create expense</h3>
						{#if expenseWarnings.length > 0}
							<p class="text-base-content/70">Expense created.</p>
							<ul class="m-0 mb-3 pl-5">
								{#each expenseWarnings as w, i (i)}
									<li class="text-warning">{w}</li>
								{/each}
							</ul>
							<Button variant="secondary" onclick={() => (open = false)}>Done</Button>
						{:else if !showExpenseForm}
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
								error={expenseError}
								{onReceiptUpload}
								onSubmit={createExpense}
							/>
						{/if}
					</section>
				{/if}

				<section>
					<h3 class="m-0 mb-2 text-sm text-base-content/70 uppercase">Ignore</h3>
					<div class="flex flex-wrap items-center gap-3 [&_select]:min-w-0 [&_select]:flex-[1_1_12rem]">
						<Select id="ignore-reason" bind:value={ignoreReason} options={IGNORE_REASON_OPTIONS} />
						<Button variant="secondary" disabled={actionBusy} onclick={ignoreLine}>
							Ignore line
						</Button>
					</div>
					{#if ignoreError}
						<p class="alert alert-error alert-soft" role="alert">{ignoreError}</p>
					{/if}
				</section>
			{:else}
				<section>
					<p>
						{line.status === 'ignored' ? 'Ignored' : 'Matched'}
						{#if line.match}: {line.match.label}{/if}
					</p>
					<Button variant="secondary" disabled={actionBusy} onclick={unmatchLine}>Unmatch</Button>
					{#if unmatchError}
						<p class="alert alert-error alert-soft" role="alert">{unmatchError}</p>
					{/if}
				</section>
			{/if}
		</div>
	{/if}
</Drawer>
