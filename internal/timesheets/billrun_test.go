package timesheets_test

import (
	"strings"
	"testing"
	"time"

	"shigoto/internal/core"
	"shigoto/internal/timesheets"
)

func mustDate(t *testing.T, s string) time.Time {
	t.Helper()
	d, err := time.ParseInLocation("2006-01-02", s, time.UTC)
	if err != nil {
		t.Fatalf("parsing date %q: %v", s, err)
	}
	return d
}

func TestLineFor(t *testing.T) {
	project := timesheets.Project{ID: 1, Name: "Acme rebuild", HourlyRate: 12000, GSTCode: core.GST}
	task := timesheets.Task{ID: 2, Name: "Design"}
	from := mustDate(t, "2026-11-01")
	to := mustDate(t, "2026-11-30")

	line := timesheets.LineFor(project, task, 50, from, to, true)
	wantDesc := "Acme rebuild – Design: 0h 50m @ $120.00/h (2026-11-01 – 2026-11-30)"
	if line.UnitAmount != 10000 {
		t.Fatalf("UnitAmount = %d, want 10000", line.UnitAmount)
	}
	if line.QuantityMilli != 1000 {
		t.Fatalf("QuantityMilli = %d, want 1000", line.QuantityMilli)
	}
	if line.GSTCode != "GST" {
		t.Fatalf("GSTCode = %q, want GST", line.GSTCode)
	}
	if line.ProjectID == nil || *line.ProjectID != project.ID {
		t.Fatalf("ProjectID = %v, want &%d", line.ProjectID, project.ID)
	}
	if line.Description != wantDesc {
		t.Fatalf("Description = %q, want %q", line.Description, wantDesc)
	}

	// A task-level rate override changes both the amount and the
	// description's displayed rate.
	override := core.Cents(15000)
	taskOverride := timesheets.Task{ID: 2, Name: "Design", HourlyRate: &override}
	line = timesheets.LineFor(project, taskOverride, 60, from, to, true)
	if line.UnitAmount != 15000 {
		t.Fatalf("UnitAmount (override) = %d, want 15000", line.UnitAmount)
	}
	if want := "Acme rebuild – Design: 1h 00m @ $150.00/h (2026-11-01 – 2026-11-30)"; line.Description != want {
		t.Fatalf("Description (override) = %q, want %q", line.Description, want)
	}

	cases := []struct {
		minutes int
		rate    core.Cents
		want    core.Cents
	}{
		{60, 9500, 9500},
		{7, 10000, 1167},
		{125, 15000, 31250},
	}
	for _, tc := range cases {
		rate := tc.rate
		task := timesheets.Task{ID: 2, Name: "Design", HourlyRate: &rate}
		line := timesheets.LineFor(project, task, tc.minutes, from, to, true)
		if line.UnitAmount != tc.want {
			t.Fatalf("LineFor(%d min @ %d) UnitAmount = %d, want %d", tc.minutes, tc.rate, line.UnitAmount, tc.want)
		}
	}

	// 125 minutes formats as "2h 05m" (zero-padded).
	line = timesheets.LineFor(project, timesheets.Task{Name: "Design"}, 125, from, to, true)
	if !strings.Contains(line.Description, "2h 05m") {
		t.Fatalf("125-minute description = %q, want to contain %q", line.Description, "2h 05m")
	}

	// Not GST-registered: always FRE, regardless of the project's own code.
	line = timesheets.LineFor(project, task, 50, from, to, false)
	if line.GSTCode != "FRE" {
		t.Fatalf("GSTCode (not GST-registered) = %q, want FRE", line.GSTCode)
	}
}
