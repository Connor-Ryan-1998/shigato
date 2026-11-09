export function canDismiss(busy: boolean, dirty: boolean): boolean {
	return !busy && (!dirty || window.confirm('Discard unsaved changes?'));
}

export function formValues(element: HTMLElement): string {
	return JSON.stringify([...element.querySelectorAll('input, select, textarea')].map((field) => {
		const input = field as HTMLInputElement;
		return [input.id || input.name, input.value, input.type === 'checkbox' || input.type === 'radio' ? input.checked : null];
	}));
}
