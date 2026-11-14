// The property page's heading, shared by its layout (which loads it) and
// the Details tab (which renames the property), keyed by "org/id" so a
// stale name never shows on another property.
export const propertyHeader = $state({ key: '', name: '' });
