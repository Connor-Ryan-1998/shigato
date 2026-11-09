<script lang="ts" module>
	export type NewLease = {
		property_id: number; tenants: { contact_id: number; primary: boolean }[]; start_date: string;
		frequency: string; rent_amount: number; bond_amount?: number; paid_to_date?: string; opening_arrears?: number;
	};
</script>
<script lang="ts">
	import { untrack } from 'svelte';
	import Field from '../../components/Field.svelte';
	import Select from '../../components/Select.svelte';
	import FormActions from '../../components/FormActions.svelte';
	import { parseCents, todayLocal } from '../../format';
	let { busy = false, error = '', properties, tenants, onsubmit, oncancel, onchange }: {
		busy?: boolean; error?: string; properties: { id: number; name: string }[]; tenants: { id: number; name: string }[];
		onsubmit: (lease: NewLease) => void; oncancel: () => void; onchange: () => void;
	} = $props();
	let property = $state('');
	let tenant = $state('');
	let start = $state(todayLocal());
	let frequency = $state('weekly');
	let rent = $state('');
	let bond = $state('');
	let coTenants = $state<string[]>([]);
	let paidTo = $state('');
	let arrears = $state('');
	let validation = $state('');
	$effect(() => { const p = properties; const t = tenants; untrack(() => { property ||= String(p[0]?.id ?? ''); tenant ||= String(t[0]?.id ?? ''); }); });
	function save(event: SubmitEvent) {
		event.preventDefault();
		if (busy) return;
		const rentCents = parseCents(String(rent));
		const bondCents = bond === '' ? 0 : parseCents(String(bond));
		const arrearsCents = arrears === '' ? undefined : parseCents(String(arrears));
		if (!property || !tenant || rentCents === null || rentCents < 0 || bondCents === null || bondCents < 0 || arrearsCents === null || (arrearsCents !== undefined && arrearsCents < 0)) {
			validation = 'Choose a property and tenant, and enter valid non-negative amounts with up to two decimal places.';
			return;
		}
		validation = '';
		onsubmit({ property_id: Number(property), tenants: [{ contact_id: Number(tenant), primary: true }, ...coTenants.filter((id) => id !== tenant).map((id) => ({ contact_id: Number(id), primary: false }))], start_date: start, frequency, rent_amount: rentCents, bond_amount: bondCents || undefined, paid_to_date: paidTo || undefined, opening_arrears: arrearsCents });
	}
</script>

<form onsubmit={save} class="grid max-w-2xl gap-4" oninput={onchange} onchange={onchange}>
	{#if error || validation}<p role="alert" class="alert alert-error alert-soft">{error || validation}</p>{/if}
	<fieldset disabled={busy} class="fieldset grid gap-4 rounded-box border border-base-300 bg-base-100 p-4">
		<legend class="fieldset-legend">Lease details</legend>
		<Field label="Property" id="lease-property"><Select id="lease-property" bind:value={property} options={properties.map((p) => ({ value: String(p.id), label: p.name }))} /></Field>
		<Field label="Tenant" id="lease-tenant"><Select id="lease-tenant" bind:value={tenant} options={tenants.map((t) => ({ value: String(t.id), label: t.name }))} onchange={(value) => { coTenants = coTenants.filter((id) => id !== value); }} /></Field>
		<fieldset class="fieldset rounded-box border border-base-300 p-3"><legend class="fieldset-legend">Co-tenants (optional)</legend>
			<p>The selected Tenant is primary and receives invoices.</p>
			{#each tenants.filter((t) => String(t.id) !== tenant) as t (t.id)}<label class="label"><input class="checkbox" type="checkbox" value={String(t.id)} bind:group={coTenants} /> Co-tenant: {t.name}</label>{/each}
		</fieldset>
		<Field label="Start" id="lease-start"><input class="input w-full" id="lease-start" type="date" bind:value={start} required /></Field>
		<Field label="Frequency" id="lease-frequency"><Select id="lease-frequency" bind:value={frequency} options={[{ value: 'weekly', label: 'Weekly' }, { value: 'fortnightly', label: 'Fortnightly' }, { value: 'monthly', label: 'Monthly' }]} /></Field>
		<Field label="Rent ($)" id="lease-rent"><input class="input w-full" id="lease-rent" inputmode="decimal" bind:value={rent} required /></Field>
		<Field label="Bond ($)" id="lease-bond"><input class="input w-full" id="lease-bond" inputmode="decimal" bind:value={bond} /></Field>
		<Field label="Paid-to date" id="lease-paid-to" hint="Must be the last day of a rent period; leave blank for a new lease."><input class="input w-full" id="lease-paid-to" type="date" bind:value={paidTo} /></Field>
		<Field label="Opening arrears ($)" id="lease-arrears" hint="Positive arrears create a separate draft invoice; it is not sent automatically by this form."><input class="input w-full" id="lease-arrears" inputmode="decimal" bind:value={arrears} /></Field>
	</fieldset>
	<FormActions {busy} submitLabel="Create lease" {oncancel} />
</form>
