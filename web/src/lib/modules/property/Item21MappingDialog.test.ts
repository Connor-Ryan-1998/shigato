import { expect, it, vi } from 'vitest';
import { fireEvent, render, screen } from '@testing-library/svelte';
import { apiMock, mockApi } from '../../test/mock-api';
import { signIn } from '../../test/session';
import Item21MappingDialog from './Item21MappingDialog.svelte';

it('saves only changed categories with a full category body, and offers no depreciation labels', async () => {
	signIn();
	mockApi({
		'GET /api/v1/orgs/acme/categories': [
			{ id: 2, name: 'Council rates', type: 'expense', default_gst_code: 'N-T', capital: false, item21_label: 'council_rates' },
			{ id: 4, name: 'Travel', type: 'expense', default_gst_code: 'GST', capital: false, item21_label: null }
		],
		'PUT /api/v1/orgs/acme/categories/4': {}
	});
	const onsaved = vi.fn();
	render(Item21MappingDialog, { open: true, org: 'acme', onsaved });
	const travel = (await screen.findByLabelText('Travel')) as HTMLSelectElement;
	expect([...travel.options].map((o) => o.value)).not.toContain('capital_works');
	await fireEvent.change(travel, { target: { value: 'sundry' } });
	await fireEvent.click(screen.getByRole('button', { name: 'Save labels' }));
	await vi.waitFor(() => expect(onsaved).toHaveBeenCalled());
	const puts = apiMock.mock.calls.filter(([m]) => m === 'PUT');
	expect(puts).toEqual([['PUT', '/api/v1/orgs/acme/categories/4', { name: 'Travel', type: 'expense', default_gst_code: 'GST', capital: false, item21_label: 'sundry' }]]);
});
