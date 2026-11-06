package httpapi

import "testing"

// TestDashboardModulePermCoversEveryDashboard covers ruling 41: a module
// whose Dashboard is non-nil but has no dashboardModulePerm entry would be
// silently left off every dashboard, so each one must be mapped to its
// read permission.
func TestDashboardModulePermCoversEveryDashboard(t *testing.T) {
	for _, m := range modules {
		if m.Dashboard == nil {
			continue
		}
		if _, ok := dashboardModulePerm[m.Name]; !ok {
			t.Errorf("module %q has a Dashboard but no dashboardModulePerm entry", m.Name)
		}
	}
}
