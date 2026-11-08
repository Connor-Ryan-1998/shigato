<script lang="ts">
	// This week's hours per day (Mon-Sun), stacked billed / unbilled /
	// fixed-fee. Plain CSS bars; the sr-only summary carries the numbers.
	import { todayLocal } from '../../format';
	import { billingStatus, hhmm, periodRange, type BillingStatus, type Entry } from './types';

	let { entries, today = todayLocal() }: { entries: Entry[]; today?: string } = $props();

	const SERIES: { key: BillingStatus; label: string; cls: string }[] = [
		{ key: 'billed', label: 'Billed hours', cls: 'bg-primary' },
		{ key: 'unbilled', label: 'Unbilled hours', cls: 'bg-warning' },
		{ key: 'fixed', label: 'Fixed-fee hours', cls: 'bg-base-content/30' }
	];

	const days = $derived.by(() => {
		const { from } = periodRange('this_week', today)!;
		const [y, m, d] = from.split('-').map(Number);
		return Array.from({ length: 7 }, (_, i) => {
			const day = new Date(y, m - 1, d + i);
			const iso = `${day.getFullYear()}-${String(day.getMonth() + 1).padStart(2, '0')}-${String(day.getDate()).padStart(2, '0')}`;
			const minutes: Record<BillingStatus, number> = { billed: 0, unbilled: 0, fixed: 0 };
			for (const e of entries) if (e.work_date === iso) minutes[billingStatus(e)] += e.minutes;
			return {
				iso,
				label: day.toLocaleDateString(undefined, { weekday: 'short', day: '2-digit' }),
				minutes,
				total: minutes.billed + minutes.unbilled + minutes.fixed
			};
		});
	});
	// Round the scale up to whole hours (at least 1h) so bars have headroom.
	const maxHours = $derived(Math.max(1, Math.ceil(Math.max(...days.map((d) => d.total)) / 60)));
</script>

<figure class="w-full">
	<figcaption class="mb-2 text-sm font-semibold">Project hours this week</figcaption>
	<div class="flex h-40 gap-2" aria-hidden="true">
		<div class="flex flex-col justify-between text-right text-xs text-base-content/60 tabular-nums">
			<span>{maxHours}h</span><span>{maxHours / 2}h</span><span>0</span>
		</div>
		{#each days as day (day.iso)}
			<div class="flex flex-1 flex-col justify-end border-b border-base-300" title={`${day.label}: ${hhmm(day.total)}`}>
				{#each [...SERIES].reverse() as s (s.key)}
					{#if day.minutes[s.key] > 0}
						<div class={['w-full first:rounded-t', s.cls]} style:height="{(day.minutes[s.key] / 60 / maxHours) * 100}%"></div>
					{/if}
				{/each}
			</div>
		{/each}
	</div>
	<div class="ml-8 flex gap-2 text-center text-xs text-base-content/60" aria-hidden="true">
		{#each days as day (day.iso)}<span class="flex-1">{day.label}</span>{/each}
	</div>
	<div class="mt-2 flex flex-wrap justify-center gap-4 text-xs" aria-hidden="true">
		{#each SERIES as s (s.key)}
			<span class="flex items-center gap-1"><span class={['inline-block h-2 w-4 rounded', s.cls]}></span>{s.label}</span>
		{/each}
	</div>
	<ul class="sr-only">
		{#each days as day (day.iso)}
			<li>{day.label}: {hhmm(day.minutes.billed)} billed, {hhmm(day.minutes.unbilled)} unbilled, {hhmm(day.minutes.fixed)} fixed fee</li>
		{/each}
	</ul>
</figure>
