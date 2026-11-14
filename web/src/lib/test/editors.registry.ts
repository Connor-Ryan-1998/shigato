// Components the editor contract can mount, by the name editors/*.yaml use.

import ExpenseNewPage from '../../routes/[org]/expenses/new/+page.svelte';
import InvoicePage from '../../routes/[org]/invoices/[id]/+page.svelte';
import InvoiceNewPage from '../../routes/[org]/invoices/new/+page.svelte';
import ContactDialog from '../modules/core/contacts/ContactDialog.svelte';
import LogTimeDialog from '../modules/timesheets/LogTimeDialog.svelte';
import SettingsPage from '../../routes/[org]/settings/+page.svelte';
import LeasePage from '../../routes/[org]/leases/[id]/+page.svelte';
import LeaseNewPage from '../../routes/[org]/leases/new/+page.svelte';
import ProjectDialog from '../modules/timesheets/ProjectDialog.svelte';
import BillingRunDialog from '../modules/timesheets/BillingRunDialog.svelte';
import PropertyDetailsPage from '../../routes/[org]/properties/[id]/+page.svelte';
import InsuranceDialog from '../modules/property/InsuranceDialog.svelte';
import DepreciationItemDialog from '../modules/property/DepreciationItemDialog.svelte';
import Item21MappingDialog from '../modules/property/Item21MappingDialog.svelte';
import PropertyNotesPage from '../../routes/[org]/properties/[id]/notes/+page.svelte';

export const components = {
	BillingRunDialog,
	ContactDialog,
	DepreciationItemDialog,
	ExpenseNewPage,
	InsuranceDialog,
	Item21MappingDialog,
	InvoiceNewPage,
	InvoicePage,
	LeaseNewPage,
	LeasePage,
	LogTimeDialog,
	ProjectDialog,
	PropertyDetailsPage,
	PropertyNotesPage,
	SettingsPage
} as const;
