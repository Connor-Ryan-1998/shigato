// The editor contract: the AGENTS.md rules every editor keeps, as one check
// per key. An editor's YAML (editors/*.yaml) says how to drive it; each key
// in its `contract` runs the matching check below. A key that doesn't apply
// goes under `skip` with the reason.

import { expect, vi } from 'vitest';
import { tick, type Component } from 'svelte';
import { fireEvent, render, screen, waitFor, within } from '@testing-library/svelte';
import { parse } from 'yaml';
import { apiError, apiMock, mockApi, pending } from './mock-api';
import { beforeNavigate, goto, setPage } from './app.svelte';
import { signIn } from './session';
import { session } from '../session.svelte';
import { components } from './editors.registry';

export const CONTRACT_KEYS = [
	'blocks_duplicate_submit', // while saving, filled fields are disabled and a second submit sends nothing
	'preserves_input_on', // a save rejected with this status keeps every typed value, editable and retryable
	'cancel', // this button closes the editor without a write
	'busy_dismiss_blocked', // while saving, Cancel and Escape leave the editor open, unprompted
	'dirty_dismiss_confirms', // Escape with edits asks to discard; refusing keeps it open
	'dirty_navigation_blocked' // leaving the page with edits asks to discard; refusing cancels
] as const;
type Key = (typeof CONTRACT_KEYS)[number];

const TOP_KEYS = ['id', 'component', 'props', 'callbacks', 'page', 'session', 'modules', 'api', 'ready', 'open', 'fill',
	'submit', 'request', 'contract', 'skip'] as const;

export type Editor = {
	file: string;
	id: string;
	component: keyof typeof components;
	props?: Record<string, unknown>;
	callbacks?: string[]; // props given a vi.fn()
	page?: Record<string, string>; // route params
	session?: { role?: string; perms?: string[] }; // default: owner of acme
	modules?: string[]; // enabled modules (GET /meta)
	api?: Record<string, unknown>; // mockApi routes for everything but `request`
	ready?: string; // text to wait for before interacting
	open?: string; // button that opens the editor
	fill: Record<string, string>; // exact label -> value ("true"/"false" for a checkbox)
	submit: string;
	request: string; // 'METHOD /path' the save sends
	contract: Partial<Record<Key, unknown>>;
	skip?: Partial<Record<Key, string>>;
};

export function parseEditor(source: string, file: string): Editor {
	const e = parse(source) as Editor;
	for (const k of Object.keys(e)) {
		if (!(TOP_KEYS as readonly string[]).includes(k)) throw new Error(`${file}: unknown key "${k}"`);
	}
	for (const k of ['id', 'component', 'fill', 'submit', 'request', 'contract'] as const) {
		if (e[k] === undefined) throw new Error(`${file}: missing "${k}"`);
	}
	if (!(e.component in components)) throw new Error(`${file}: unknown component "${e.component}"`);
	for (const k of [...Object.keys(e.contract), ...Object.keys(e.skip ?? {})]) {
		if (!(CONTRACT_KEYS as readonly string[]).includes(k)) throw new Error(`${file}: unknown contract key "${k}"`);
	}
	for (const k of CONTRACT_KEYS) {
		const checked = k in e.contract;
		const skipped = typeof e.skip?.[k] === 'string' && e.skip[k] !== '';
		if (checked === skipped) {
			throw new Error(checked
				? `${file}: contract key "${k}" is both checked and skipped`
				: `${file}: contract key "${k}" is neither checked nor skipped with a reason`);
		}
	}
	return { ...e, file };
}

const label = (e: Editor, key: Key) => `${e.file} (${e.id}) ${key}`;

// mount renders the editor with `request` answered by `save`, opens it and
// waits until it is ready to edit.
async function mount(e: Editor, save: unknown) {
	mockApi({ ...e.api, [e.request]: save } as Parameters<typeof mockApi>[0]);
	if (e.page) setPage({ params: e.page });
	// Editors only guard unsaved work for a signed-in user, as in the app.
	signIn(e.session ?? {});
	session.setModules(e.modules ?? []);
	const props: Record<string, unknown> = structuredClone(e.props ?? {});
	for (const name of e.callbacks ?? []) props[name] = vi.fn();
	// YAML props are untyped data; the contract run itself checks they fit.
	render(components[e.component] as unknown as Component<Record<string, unknown>>, { props });
	if (e.ready) await screen.findByText(e.ready);
	if (e.open) await fireEvent.click(await screen.findByRole('button', { name: e.open }));
	// The editor's submit may share its name with the page button that opened
	// it ("Record payment"); prefer the one in an open dialog.
	const buttons = await screen.findAllByRole('button', { name: e.submit });
	const button = buttons.find((b) => b.closest('dialog')?.open) ?? buttons[buttons.length - 1];
	// Editors snapshot their clean values a tick after opening.
	await tick();
	// Kept, because busy editors relabel their submit button ("Saving…").
	return { button, form: button.closest('form'), dialog: button.closest('dialog') };
}

type Mounted = Awaited<ReturnType<typeof mount>>;

// cancelButton is looked up inside the editor: its page may have its own.
function cancelButton(m: Mounted, name: string): HTMLButtonElement {
	const scope = m.dialog ?? m.form;
	return (scope ? within(scope as HTMLElement) : screen).getByRole('button', { name }) as HTMLButtonElement;
}

const field = (name: string) => screen.getByLabelText(name, { exact: true }) as HTMLInputElement;

// controls are the editor's own form fields.
const controls = (m: Mounted) =>
	[...(m.form ?? m.dialog ?? document.body).querySelectorAll<HTMLInputElement>('input:not([type=hidden]), select, textarea')];

// A checkbox is filled with "true" or "false"; everything else with its value.
async function fill(e: Editor) {
	for (const [name, value] of Object.entries(e.fill)) {
		const el = field(name);
		if (el.type === 'checkbox') {
			if (el.checked !== (value === 'true')) await fireEvent.click(el);
		} else if (el.tagName === 'SELECT') await fireEvent.change(el, { target: { value } });
		else await fireEvent.input(el, { target: { value } });
	}
}

const valueOf = (el: HTMLInputElement) => (el.type === 'checkbox' ? String(el.checked) : el.value);

// submit sends the form directly, as a user pressing Enter would, so a
// guard that only disables the button can't hide a missing in-flight check.
async function submit(e: Editor, m: Mounted) {
	if (m.form) await fireEvent.submit(m.form);
	else await fireEvent.click(m.button);
}

const writes = (e: Editor) => apiMock.mock.calls.filter(([m, p]) => `${m} ${p}` === e.request);

function dialogOf(e: Editor, m: Mounted): HTMLDialogElement {
	if (!m.dialog) throw new Error(`${e.file} (${e.id}): not a dialog editor; skip this key`);
	return m.dialog;
}

// A dialog editor is closed when its dialog is; a page editor when its form
// has gone.
function isClosed(m: Mounted): boolean {
	return m.dialog ? !m.dialog.open : !m.form?.isConnected;
}

async function escape(dialog: HTMLDialogElement) {
	dialog.dispatchEvent(new Event('cancel', { cancelable: true }));
	await tick();
}

export const contract: Record<Key, (e: Editor) => Promise<void>> = {
	async blocks_duplicate_submit(e) {
		const m = await mount(e, pending());
		await fill(e);
		await submit(e, m);
		await waitFor(() => expect(writes(e), label(e, 'blocks_duplicate_submit')).toHaveLength(1));
		// ui-standards: disable affected inputs while saving - every control.
		for (const control of controls(m)) {
			const name = control.labels?.[0]?.textContent?.trim() || control.name || control.id;
			expect(control.matches(':disabled'), `${label(e, 'blocks_duplicate_submit')}: "${name}" editable while saving`).toBe(true);
		}
		await submit(e, m);
		await tick();
		expect(writes(e), `${label(e, 'blocks_duplicate_submit')}: second submit while pending`).toHaveLength(1);
	},

	async preserves_input_on(e) {
		const status = Number(e.contract.preserves_input_on);
		const m = await mount(e, apiError(status, 'contract: save rejected'));
		await fill(e);
		await submit(e, m);
		await waitFor(() => expect(writes(e), label(e, 'preserves_input_on')).toHaveLength(1));
		await screen.findByText('contract: save rejected');
		expect(isClosed(m), `${label(e, 'preserves_input_on')}: editor closed after a rejected save`).toBe(false);
		for (const [name, value] of Object.entries(e.fill)) {
			expect(valueOf(field(name)), `${label(e, 'preserves_input_on')}: "${name}"`).toBe(value);
			expect(field(name).matches(':disabled'), `${label(e, 'preserves_input_on')}: "${name}" still locked`).toBe(false);
		}
		expect(within((m.dialog ?? m.form ?? document.body) as HTMLElement).getByRole('button', { name: e.submit }), `${label(e, 'preserves_input_on')}: cannot retry`)
			.toHaveProperty('disabled', false);
	},

	async cancel(e) {
		const m = await mount(e, {});
		await fill(e);
		await fireEvent.click(cancelButton(m, String(e.contract.cancel)));
		await tick();
		expect(writes(e), `${label(e, 'cancel')}: wrote on cancel`).toHaveLength(0);
		expect(isClosed(m), `${label(e, 'cancel')}: editor still open`).toBe(true);
	},

	async busy_dismiss_blocked(e) {
		const m = await mount(e, pending());
		await fill(e);
		await submit(e, m);
		await waitFor(() => expect(writes(e)).toHaveLength(1));
		// `busy_dismiss_blocked: <button>` names a Cancel the `cancel` key doesn't
		// (a page editor whose Cancel resets instead of closing).
		const value = e.contract.busy_dismiss_blocked;
		const cancelName = typeof value === 'string' ? value : e.contract.cancel ? String(e.contract.cancel) : null;
		if (!cancelName && !m.dialog) throw new Error(`${label(e, 'busy_dismiss_blocked')}: no Cancel or dialog to check`);
		if (cancelName) {
			// ui-standards: a pending save prevents dismissal. Clicking a disabled
			// Cancel does nothing, so this holds whether it's disabled or refused.
			await fireEvent.click(cancelButton(m, cancelName));
			await tick();
			expect(isClosed(m), `${label(e, 'busy_dismiss_blocked')}: Cancel dismissed the editor while saving`).toBe(false);
			expect(goto, `${label(e, 'busy_dismiss_blocked')}: Cancel navigated away while saving`).not.toHaveBeenCalled();
			for (const [name, value] of Object.entries(e.fill)) {
				expect(valueOf(field(name)), `${label(e, 'busy_dismiss_blocked')}: Cancel discarded "${name}" while saving`).toBe(value);
			}
		}
		if (!m.dialog) return;
		const dialog = m.dialog;
		await escape(dialog);
		expect(dialog.open, `${label(e, 'busy_dismiss_blocked')}: dialog closed while saving`).toBe(true);
		expect(window.confirm, `${label(e, 'busy_dismiss_blocked')}: prompted while saving`).not.toHaveBeenCalled();
	},

	async dirty_dismiss_confirms(e) {
		const m = await mount(e, {});
		await fill(e);
		vi.mocked(window.confirm).mockReturnValue(false);
		const dialog = dialogOf(e, m);
		await escape(dialog);
		expect(window.confirm, `${label(e, 'dirty_dismiss_confirms')}: no discard prompt`).toHaveBeenCalled();
		expect(dialog.open, `${label(e, 'dirty_dismiss_confirms')}: closed after refusing discard`).toBe(true);
	},

	async dirty_navigation_blocked(e) {
		await mount(e, {});
		await fill(e);
		vi.mocked(window.confirm).mockReturnValue(false);
		expect(beforeNavigate, `${label(e, 'dirty_navigation_blocked')}: no navigation guard`).toHaveBeenCalled();
		const cancel = vi.fn();
		for (const [guard] of beforeNavigate.mock.calls) {
			(guard as (nav: unknown) => void)({ to: { url: new URL('http://localhost/elsewhere') }, cancel });
		}
		expect(cancel, `${label(e, 'dirty_navigation_blocked')}: navigation not cancelled`).toHaveBeenCalled();
	}
};
