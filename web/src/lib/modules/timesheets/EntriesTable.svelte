<script lang="ts">
	import DateText from '../../components/DateText.svelte';
	import EmptyState from '../../components/EmptyState.svelte';
	import { billingStatus, hhmm, type Entry } from './types';

	let {
		entries,
		org,
		showProject = true,
		editable,
		onedit,
		ondelete,
		busy = false
	}: {
		entries: Entry[];
		org: string;
		// Project and customer columns (off on a project's own page).
		showProject?: boolean;
		editable: (e: Entry) => boolean;
		onedit: (e: Entry) => void;
		ondelete: (e: Entry) => void;
		busy?: boolean;
	} = $props();

	const STATUS = {
		billed: { label: 'Invoiced', cls: 'text-success' },
		unbilled: { label: 'Unbilled', cls: 'text-warning' },
		fixed: { label: 'Fixed fee', cls: 'text-base-content/60' }
	};
	const total = $derived(entries.reduce((sum, e) => sum + e.minutes, 0));
</script>

{#if entries.length === 0}
	<EmptyState title="No time logged" description="Nothing matches these filters." />
{:else}
	<div class="overflow-x-auto rounded-box border border-base-300 bg-base-100">
		<table class="table">
			<thead>
				<tr>
					<th>Date</th>
					{#if showProject}<th>Project</th><th>Customer</th>{/if}
					<th>Task</th>
					<th>User</th>
					<th class="text-right">Time</th>
					<th>Billing status</th>
					<th><span class="sr-only">Actions</span></th>
				</tr>
			</thead>
			<tbody>
				{#each entries as e (e.id)}
					{@const status = STATUS[billingStatus(e)]}
					<tr>
						<td class="whitespace-nowrap"><DateText iso={e.work_date} /></td>
						{#if showProject}
							<td><a class="link link-primary" href={`/${org}/projects/${e.project_id}`}>{e.project_name}</a></td>
							<td>{e.client_name}</td>
						{/if}
						<td class="max-w-xs">
							<div>{e.task_name}</div>
							{#if e.note}<div class="line-clamp-1 text-sm text-base-content/70" title={e.note}>{e.note}</div>{/if}
						</td>
						<td>{e.user_name}</td>
						<td class="text-right tabular-nums">{hhmm(e.minutes)}</td>
						<td class={status.cls}>{status.label}</td>
						<td class="whitespace-nowrap text-right">
							{#if editable(e)}
								<button
									type="button"
									class="btn btn-ghost btn-xs"
									disabled={busy}
									aria-label={`Edit entry ${e.id}`}
									onclick={() => onedit(e)}>Edit</button
								>
								<button
									type="button"
									class="btn btn-ghost btn-xs text-error"
									disabled={busy}
									aria-label={`Delete entry ${e.id}`}
									onclick={() => ondelete(e)}>Delete</button
								>
							{/if}
						</td>
					</tr>
				{/each}
			</tbody>
			<tfoot>
				<tr>
					<th colspan={showProject ? 5 : 3}>Total</th>
					<th class="text-right tabular-nums">{hhmm(total)}</th>
					<th colspan="2"></th>
				</tr>
			</tfoot>
		</table>
	</div>
{/if}
