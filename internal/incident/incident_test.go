package incident

import (
	"strings"
	"testing"
	"time"

	"github.com/Erfan-Abouei/Abouei_Checker/internal/config"
	"github.com/Erfan-Abouei/Abouei_Checker/internal/probe"
)

func backendWithPolicy(failure, recovery int, enabled, down, recoveryNotify bool) config.Backend {
	return config.Backend{
		ID:                "api",
		Name:              "API",
		FailureThreshold:  failure,
		RecoveryThreshold: recovery,
		Notifications: config.Policy{
			Enabled:          enabled,
			NotifyOnDown:     down,
			NotifyOnRecovery: recoveryNotify,
		},
	}
}

func failedProbe() probe.Result {
	return probe.Result{
		BackendID:     "api",
		Name:          "API",
		Outcome:       probe.OutcomeUnhealthy,
		HTTPStatus:    503,
		FailureReason: probe.ReasonStatusMismatch,
		Error:         "unexpected status code 503",
		ObservedAt:    time.Now().UTC(),
	}
}

func healthyProbe() probe.Result {
	return probe.Result{
		BackendID:  "api",
		Name:       "API",
		Outcome:    probe.OutcomeHealthy,
		HTTPStatus: 200,
		ObservedAt: time.Now().UTC(),
	}
}

func TestSubThresholdFailureCreatesNothing(t *testing.T) {
	backend := backendWithPolicy(3, 1, true, true, true)
	outcome := Evaluate(failedProbe(), backend, Streaks{}, "", time.Now().UTC())
	if outcome.Transition != TransitionNone || outcome.EventType != "" {
		t.Fatalf("sub-threshold must not transition: %+v", outcome)
	}
	if outcome.Streaks.Failures != 1 || outcome.Streaks.Successes != 0 {
		t.Fatalf("streaks = %+v", outcome.Streaks)
	}
	if outcome.Streaks.FirstFailureAt.IsZero() {
		t.Fatal("first failure time must be set when the streak starts")
	}
}

func TestThresholdReachedOpensIncident(t *testing.T) {
	backend := backendWithPolicy(3, 1, true, true, true)
	now := time.Now().UTC()
	outcome := Evaluate(failedProbe(), backend, Streaks{Failures: 2}, "", now)
	if outcome.Transition != TransitionOpened {
		t.Fatalf("transition = %q", outcome.Transition)
	}
	if outcome.IncidentID == "" || outcome.EventType != EventOpened {
		t.Fatalf("outcome = %+v", outcome)
	}
	if outcome.Streaks.Failures != 3 {
		t.Fatalf("failures = %d", outcome.Streaks.Failures)
	}
}

func TestPolicyDisabledStillOpensWithoutEvent(t *testing.T) {
	backend := backendWithPolicy(1, 1, false, false, false)
	outcome := Evaluate(failedProbe(), backend, Streaks{}, "", time.Now().UTC())
	if outcome.Transition != TransitionOpened || outcome.IncidentID == "" {
		t.Fatalf("incident must open even with notifications disabled: %+v", outcome)
	}
	if outcome.EventType != "" {
		t.Fatalf("disabled policy must not enqueue an event: %+v", outcome)
	}
}

func TestThresholdOne(t *testing.T) {
	down := backendWithPolicy(1, 1, true, true, true)
	opened := Evaluate(failedProbe(), down, Streaks{}, "", time.Now().UTC())
	if opened.Transition != TransitionOpened {
		t.Fatalf("threshold 1 must open immediately: %+v", opened)
	}
	recovered := Evaluate(healthyProbe(), down, Streaks{}, opened.IncidentID, time.Now().UTC())
	if recovered.Transition != TransitionRecovered || recovered.EventType != EventRecovered {
		t.Fatalf("threshold 1 must recover immediately: %+v", recovered)
	}
}

func TestStillOpenDoesNotDuplicate(t *testing.T) {
	backend := backendWithPolicy(2, 1, true, true, true)
	outcome := Evaluate(failedProbe(), backend, Streaks{Failures: 5}, "incident-1", time.Now().UTC())
	if outcome.Transition != TransitionStillOpen || outcome.EventType != "" {
		t.Fatalf("repeated failure must not duplicate Down: %+v", outcome)
	}
	if outcome.IncidentID != "incident-1" {
		t.Fatalf("incident = %q", outcome.IncidentID)
	}
}

func TestRecoveryThresholdGreaterThanOne(t *testing.T) {
	backend := backendWithPolicy(2, 3, true, true, true)
	first := Evaluate(healthyProbe(), backend, Streaks{}, "incident-1", time.Now().UTC())
	if first.Transition != TransitionNone || first.Streaks.Successes != 1 {
		t.Fatalf("below recovery threshold: %+v", first)
	}
	second := Evaluate(healthyProbe(), backend, first.Streaks, "incident-1", time.Now().UTC())
	if second.Transition != TransitionNone || second.Streaks.Successes != 2 {
		t.Fatalf("still below recovery threshold: %+v", second)
	}
	third := Evaluate(healthyProbe(), backend, second.Streaks, "incident-1", time.Now().UTC())
	if third.Transition != TransitionRecovered || third.EventType != EventRecovered {
		t.Fatalf("recovery threshold must close: %+v", third)
	}
}

func TestHealthyWithoutIncidentResets(t *testing.T) {
	backend := backendWithPolicy(3, 1, true, true, true)
	outcome := Evaluate(healthyProbe(), backend, Streaks{Failures: 2}, "", time.Now().UTC())
	if outcome.Transition != TransitionNone || outcome.Streaks.Failures != 0 {
		t.Fatalf("healthy without incident resets: %+v", outcome)
	}
}

func TestFailedProbeResetsRecoveryStreak(t *testing.T) {
	backend := backendWithPolicy(3, 2, true, true, true)
	outcome := Evaluate(failedProbe(), backend, Streaks{Successes: 1}, "incident-1", time.Now().UTC())
	if outcome.Streaks.Successes != 0 || outcome.Transition != TransitionStillOpen {
		t.Fatalf("failed probe resets recovery streak: %+v", outcome)
	}
}

func TestCanceledLeavesEverything(t *testing.T) {
	backend := backendWithPolicy(1, 1, true, true, true)
	canceled := probe.Result{BackendID: "api", Outcome: probe.OutcomeCanceled, ObservedAt: time.Now().UTC()}
	before := Streaks{Failures: 2, Successes: 1}
	outcome := Evaluate(canceled, backend, before, "incident-1", time.Now().UTC())
	if outcome.Transition != TransitionNone || outcome.Streaks != before {
		t.Fatalf("canceled must not change state: %+v", outcome)
	}
}

func TestReminderPolicy(t *testing.T) {
	now := time.Now().UTC()
	base := ReminderDecision{
		OpenIncidentID:    "incident-1",
		Enabled:           true,
		NextReminderAt:    now.Add(-time.Minute),
		HasNextReminderAt: true,
		ReminderCount:     0,
		LastSequence:      1,
	}
	enqueue, sequence := ShouldRemind(base, now)
	if !enqueue || sequence != 2 {
		t.Fatalf("due reminder must enqueue with sequence 2: %v %d", enqueue, sequence)
	}

	future := base
	future.NextReminderAt = now.Add(time.Hour)
	if enqueue, _ := ShouldRemind(future, now); enqueue {
		t.Fatal("reminder before its interval must not enqueue")
	}

	blocked := base
	blocked.BlockingEvents = 1
	if enqueue, _ := ShouldRemind(blocked, now); enqueue {
		t.Fatal("reminder with pending earlier events must wait")
	}

	capped := base
	capped.ReminderMax = 2
	capped.ReminderCount = 2
	if enqueue, _ := ShouldRemind(capped, now); enqueue {
		t.Fatal("reminder at cap must not enqueue")
	}

	closed := base
	closed.OpenIncidentID = ""
	if enqueue, _ := ShouldRemind(closed, now); enqueue {
		t.Fatal("reminder without an open incident must not enqueue")
	}

	disabled := base
	disabled.Enabled = false
	if enqueue, _ := ShouldRemind(disabled, now); enqueue {
		t.Fatal("reminder with disabled policy must not enqueue")
	}

	unscheduled := base
	unscheduled.HasNextReminderAt = false
	if enqueue, sequence := ShouldRemind(unscheduled, now); !enqueue || sequence != 2 {
		t.Fatalf("unscheduled reminder must be due immediately: %v %d", enqueue, sequence)
	}
}

func TestNewIDIsUUIDv4(t *testing.T) {
	seen := map[string]struct{}{}
	for i := 0; i < 100; i++ {
		id := NewID()
		if len(id) != 36 || id[14] != '4' {
			t.Fatalf("id %q is not UUIDv4", id)
		}
		if !strings.ContainsAny(string(id[19]), "89ab") {
			t.Fatalf("id %q has a bad variant", id)
		}
		if _, duplicate := seen[id]; duplicate {
			t.Fatal("duplicate ID generated")
		}
		seen[id] = struct{}{}
	}
}
