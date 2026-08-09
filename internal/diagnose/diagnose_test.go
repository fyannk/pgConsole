// Copyright 2026 The pgConsole Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package diagnose

import (
	"strings"
	"testing"
	"time"

	"github.com/fyannk/pgConsole/internal/observe"
)

var now = time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)

func input(schedules []observe.ScheduledBackupFacts, backups []observe.BackupFacts) Input {
	return Input{
		Now:        now,
		HasBackups: true,
		Backups: observe.BackupsSnapshot{
			ScheduledBackups: schedules,
			Backups:          backups,
		},
	}
}

func schedule(name, expr string) observe.ScheduledBackupFacts {
	return observe.ScheduledBackupFacts{Name: name, Schedule: expr, Method: "barmanObjectStore"}
}

// TestRunsPerDayReadsTheSixFieldCron pins the arithmetic the whole
// finding rests on, including the case it exists for: a five-field
// expression written from habit gains a field and becomes hourly.
func TestRunsPerDayReadsTheSixFieldCron(t *testing.T) {
	t.Parallel()
	cases := []struct {
		expr string
		want int
		ok   bool
	}{
		{"0 0 2 * * *", 1, true},              // daily at 02:00, written correctly
		{"0 2 * * * *", 24, true},             // the mistake: every hour at 02 past
		{"0 0 */6 * * *", 4, true},            // every six hours
		{"*/30 * * * * *", 60 * 2 * 24, true}, // every thirty seconds
		{"0 2 * * *", 0, false},               // five fields: the operator refuses it
		{"0 0 2 * * 0", 0, false},             // restricted weekday: declines
		{"0 0 1,13 * * *", 0, false},          // list: declines rather than guesses
		{"", 0, false},
		{"garbage", 0, false},
	}
	for _, tc := range cases {
		got, ok := runsPerDay(tc.expr)
		if ok != tc.ok || (ok && got != tc.want) {
			t.Errorf("runsPerDay(%q) = %d, %v; want %d, %v", tc.expr, got, ok, tc.want, tc.ok)
		}
	}
}

// TestCadenceReportsTheShiftedSchedule proves the finding fires on the
// mistake, carries the schedule verbatim, and states both rates so the
// reader can see the comparison rather than being told a conclusion.
func TestCadenceReportsTheShiftedSchedule(t *testing.T) {
	t.Parallel()
	backups := make([]observe.BackupFacts, 0, 23)
	for i := range 23 {
		backups = append(backups, observe.BackupFacts{
			Name:      "orders-backup",
			Method:    "barmanObjectStore",
			CreatedAt: now.Add(-time.Duration(i) * time.Hour),
		})
	}
	result := Run(input([]observe.ScheduledBackupFacts{schedule("orders-nightly", "0 2 * * * *")}, backups))

	if len(result.Findings) != 1 {
		t.Fatalf("findings = %d, want 1: %+v", len(result.Findings), result.Findings)
	}
	finding := result.Findings[0]
	if finding.Severity != SeverityCritical {
		t.Errorf("severity = %v, want critical", finding.Severity)
	}
	if !strings.Contains(finding.Summary, "24 times a day") {
		t.Errorf("summary does not state the implied rate: %q", finding.Summary)
	}
	// The schedule must appear exactly as written: the reader has to be
	// able to spot the missing field themselves.
	if len(finding.Evidence) != 2 {
		t.Fatalf("evidence = %+v, want the schedule and the observed count", finding.Evidence)
	}
	if !strings.Contains(finding.Evidence[0].Detail, `"0 2 * * * *"`) {
		t.Errorf("schedule not quoted verbatim: %q", finding.Evidence[0].Detail)
	}
	if finding.Evidence[0].Origin != "operator-reported" {
		t.Errorf("schedule evidence origin = %q", finding.Evidence[0].Origin)
	}
	if !strings.Contains(finding.Evidence[1].Detail, "23 backups") {
		t.Errorf("observed count missing: %q", finding.Evidence[1].Detail)
	}
	if finding.Evidence[1].Origin != "Kubernetes-observed" {
		t.Errorf("count evidence origin = %q", finding.Evidence[1].Origin)
	}
	// It proposes; it never remediates.
	if finding.Link != "/backups" {
		t.Errorf("link = %q, want a screen", finding.Link)
	}
}

// TestCadenceIgnoresACorrectSchedule proves the detector is quiet on the
// thing it must never flag. A daily backup written correctly produces no
// finding, and the check reports clear rather than nothing at all.
func TestCadenceIgnoresACorrectSchedule(t *testing.T) {
	t.Parallel()
	result := Run(input([]observe.ScheduledBackupFacts{schedule("orders-nightly", "0 0 2 * * *")}, nil))
	if len(result.Findings) != 0 {
		t.Fatalf("a correct daily schedule was flagged: %+v", result.Findings)
	}
	if len(result.Checks) != 1 || result.Checks[0].Outcome != CheckClear {
		t.Fatalf("checks = %+v, want one clear", result.Checks)
	}
}

// TestCadenceIgnoresASuspendedSchedule proves a suspended schedule is not
// reported: it is not running, so it is not costing anything.
func TestCadenceIgnoresASuspendedSchedule(t *testing.T) {
	t.Parallel()
	suspended := true
	s := schedule("orders-nightly", "0 2 * * * *")
	s.Suspended = &suspended
	result := Run(input([]observe.ScheduledBackupFacts{s}, nil))
	if len(result.Findings) != 0 {
		t.Fatalf("a suspended schedule was flagged: %+v", result.Findings)
	}
}

// TestUnobservedInputIsNotAClearResult is the honesty property of the
// whole screen: with nothing observed, the detector reports that it could
// not run. Reporting "clear" would turn missing data into a statement
// that the cluster is fine.
func TestUnobservedInputIsNotAClearResult(t *testing.T) {
	t.Parallel()
	for name, in := range map[string]Input{
		"never observed": {Now: now, HasBackups: false},
		"stale": {Now: now, HasBackups: true, Backups: observe.BackupsSnapshot{
			Stale:            true,
			ScheduledBackups: []observe.ScheduledBackupFacts{schedule("orders-nightly", "0 2 * * * *")},
		}},
	} {
		result := Run(in)
		if len(result.Findings) != 0 {
			t.Errorf("%s: reported findings from unusable input: %+v", name, result.Findings)
		}
		if len(result.Checks) != 1 || result.Checks[0].Outcome != CheckUnavailable {
			t.Fatalf("%s: checks = %+v, want one unavailable", name, result.Checks)
		}
		if result.Checks[0].Because == "" {
			t.Errorf("%s: unavailable check gave no reason", name)
		}
	}
}

// TestRunAlwaysAccountsForEveryDetector proves the screen can never show
// an empty result without also showing what was looked at. Every
// registered detector appears in Checks on every run, whatever it found.
func TestRunAlwaysAccountsForEveryDetector(t *testing.T) {
	t.Parallel()
	for _, in := range []Input{
		{Now: now},
		input(nil, nil),
		input([]observe.ScheduledBackupFacts{schedule("s", "0 2 * * * *")}, nil),
	} {
		result := Run(in)
		if len(result.Checks) != len(Detectors()) {
			t.Errorf("checks = %d, want one per detector (%d)", len(result.Checks), len(Detectors()))
		}
		for _, check := range result.Checks {
			if check.Name == "" || check.Describes == "" {
				t.Errorf("check is unnamed or undescribed: %+v", check)
			}
		}
	}
}

// TestFindingsOrderMostSevereFirst proves the ordering is stable, so a
// refresh that changes nothing does not reshuffle the screen.
func TestFindingsOrderMostSevereFirst(t *testing.T) {
	t.Parallel()
	result := Run(input([]observe.ScheduledBackupFacts{
		schedule("zulu", "0 2 * * * *"),
		schedule("alpha", "0 2 * * * *"),
	}, nil))
	if len(result.Findings) != 2 {
		t.Fatalf("findings = %d, want 2", len(result.Findings))
	}
	if result.Findings[0].ID >= result.Findings[1].ID {
		t.Errorf("equal severities not ordered by ID: %q then %q",
			result.Findings[0].ID, result.Findings[1].ID)
	}
}
