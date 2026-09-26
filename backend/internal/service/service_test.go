package service

import (
	"errors"
	"testing"

	"groundclearance/internal/constants"
	"groundclearance/internal/model"
	"groundclearance/internal/util"
)

func TestClearanceTransition(t *testing.T) {
	cases := []struct {
		from, to string
		want     bool
	}{
		{constants.ClearancePending, constants.ClearanceCleared, true},
		{constants.ClearancePending, constants.ClearanceRestricted, true},
		{constants.ClearancePending, constants.ClearanceRevoked, true},
		{constants.ClearanceCleared, constants.ClearanceRevoked, true},
		{constants.ClearanceRestricted, constants.ClearanceRevoked, true},
		{constants.ClearanceRevoked, constants.ClearanceCleared, true},
		{constants.ClearanceRevoked, constants.ClearanceRestricted, true},
		{constants.ClearanceRevoked, constants.ClearanceRevoked, false},
		{constants.ClearanceCleared, constants.ClearanceRestricted, false},
	}
	for _, item := range cases {
		if got := allowedClearanceTransition(item.from, item.to); got != item.want {
			t.Fatalf("transition %s -> %s: got %v want %v", item.from, item.to, got, item.want)
		}
	}
}

func TestTurnaroundTransitionRedLines(t *testing.T) {
	cases := []struct {
		from, to string
		want     bool
	}{
		{constants.TurnaroundOpen, constants.TurnaroundChecking, true},
		{constants.TurnaroundDecisioned, constants.TurnaroundCompleted, true},
		{constants.TurnaroundOpen, constants.TurnaroundDecisioned, false},
		{constants.TurnaroundChecking, constants.TurnaroundCompleted, false},
		{constants.TurnaroundCompleted, constants.TurnaroundOpen, false},
	}
	for _, item := range cases {
		if got := allowedTurnaroundTransition(item.from, item.to); got != item.want {
			t.Fatalf("turnaround transition %s -> %s: got %v want %v", item.from, item.to, got, item.want)
		}
	}
}

func TestRecoveryGates(t *testing.T) {
	available := func(id uint64) (*model.GroundUnit, error) {
		return &model.GroundUnit{ID: id, UnitCode: "TUG-001", State: constants.UnitAvailable}, nil
	}
	blocked := func(id uint64) (*model.GroundUnit, error) {
		return &model.GroundUnit{ID: id, UnitCode: "TUG-001", State: constants.UnitBlocked}, nil
	}
	turnaround := &model.Turnaround{ID: 9, Status: constants.TurnaroundChecking, GroundUnitIDs: model.JSONList{"1"}}
	passedRecheck := model.SafetyCheck{ID: 2, Kind: constants.CheckKindRecheck, GroundUnitID: uint64Ptr(1), CheckCode: "RC-1", Result: constants.CheckPassed}
	pendingRecheck := model.SafetyCheck{ID: 3, Kind: constants.CheckKindRecheck, GroundUnitID: uint64Ptr(1), CheckCode: "RC-2", Result: constants.CheckPending}
	failedRecheck := model.SafetyCheck{ID: 4, Kind: constants.CheckKindRecheck, GroundUnitID: uint64Ptr(1), CheckCode: "RC-3", Result: constants.CheckFailed}
	initialPassed := model.SafetyCheck{ID: 1, Kind: constants.CheckKindInitial, CheckCode: "INIT-1", Result: constants.CheckFailed}

	if blockers := evaluateRecoveryGates(turnaround, []model.SafetyCheck{initialPassed, passedRecheck}, available); len(blockers) != 0 {
		t.Fatalf("full recovery must be allowed, got blockers: %v", blockers)
	}
	if blockers := evaluateRecoveryGates(turnaround, []model.SafetyCheck{initialPassed}, available); len(blockers) != 1 {
		t.Fatalf("missing recheck must block, got: %v", blockers)
	}
	if blockers := evaluateRecoveryGates(turnaround, []model.SafetyCheck{initialPassed, pendingRecheck}, available); len(blockers) != 1 {
		t.Fatalf("pending recheck must block, got: %v", blockers)
	}
	if blockers := evaluateRecoveryGates(turnaround, []model.SafetyCheck{initialPassed, failedRecheck}, available); len(blockers) != 1 {
		t.Fatalf("failed recheck must block, got: %v", blockers)
	}
	// A failed recheck can be superseded by a later passing recheck for the
	// same ground unit; the failed record stays immutable but no longer blocks.
	superseded := passedRecheck
	superseded.ID = 5
	superseded.Sequence = 5
	if blockers := evaluateRecoveryGates(turnaround, []model.SafetyCheck{initialPassed, failedRecheck, superseded}, available); len(blockers) != 0 {
		t.Fatalf("superseded failed recheck must not block, got: %v", blockers)
	}
	if blockers := evaluateRecoveryGates(turnaround, []model.SafetyCheck{initialPassed, passedRecheck}, blocked); len(blockers) != 1 {
		t.Fatalf("unrestored equipment must block, got: %v", blockers)
	}
	decisioned := *turnaround
	decisioned.Status = constants.TurnaroundDecisioned
	if blockers := evaluateRecoveryGates(&decisioned, []model.SafetyCheck{initialPassed, passedRecheck}, available); len(blockers) != 1 {
		t.Fatalf("turnaround outside checking must block, got: %v", blockers)
	}
}

func TestRecoveryGatesRequireRecheckForEveryUnit(t *testing.T) {
	findUnit := func(id uint64) (*model.GroundUnit, error) {
		return &model.GroundUnit{ID: id, UnitCode: "U-" + string(rune('0'+id)), State: constants.UnitAvailable}, nil
	}
	row := &model.Turnaround{ID: 10, Status: constants.TurnaroundChecking, GroundUnitIDs: model.JSONList{"1", "2"}}
	recheckUnit1 := model.SafetyCheck{ID: 3, Kind: constants.CheckKindRecheck, GroundUnitID: uint64Ptr(1), CheckCode: "RC-1", Result: constants.CheckPassed}
	blockers := evaluateRecoveryGates(row, []model.SafetyCheck{recheckUnit1}, findUnit)
	if len(blockers) != 1 {
		t.Fatalf("missing recheck for the second unit must block, got: %v", blockers)
	}
	recheckUnit2 := model.SafetyCheck{ID: 4, Kind: constants.CheckKindRecheck, GroundUnitID: uint64Ptr(2), CheckCode: "RC-2", Result: constants.CheckPassed}
	if blockers := evaluateRecoveryGates(row, []model.SafetyCheck{recheckUnit1, recheckUnit2}, findUnit); len(blockers) != 0 {
		t.Fatalf("every unit rechecked and available must allow recovery, got: %v", blockers)
	}
}

func TestGroundUnitTransitionRedLines(t *testing.T) {
	if allowedUnitTransition(constants.UnitRetired, constants.UnitAvailable) {
		t.Fatal("retired equipment must be terminal")
	}
	if allowedUnitTransition(constants.UnitAvailable, constants.UnitAvailable) {
		t.Fatal("same-state equipment transition must be rejected")
	}
	if !allowedUnitTransition(constants.UnitBlocked, constants.UnitAvailable) {
		t.Fatal("authorized recovery from blocked state must remain possible")
	}
}

func TestNormalizeEvidence(t *testing.T) {
	items, err := normalizeEvidence([]string{" inspection-1.jpg ", "inspection-1.jpg", "meter-2.json"})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0] != "inspection-1.jpg" {
		t.Fatalf("unexpected normalized evidence: %#v", items)
	}
	if _, err := normalizeEvidence([]string{"   "}); err == nil {
		t.Fatal("blank evidence must be rejected")
	} else {
		var appErr *util.AppError
		if !errors.As(err, &appErr) || appErr.Code != constants.CodeValidationFailed {
			t.Fatalf("unexpected blank evidence error: %v", err)
		}
	}
}

func TestSharedEnums(t *testing.T) {
	if !constants.IsValidUnitState(constants.UnitInspection) || constants.IsValidUnitState("broken") {
		t.Fatal("unit state validation mismatch")
	}
	if !constants.IsValidRiskLevel(constants.RiskCritical) || constants.IsValidRiskLevel("urgent") {
		t.Fatal("risk validation mismatch")
	}
}

func uint64Ptr(value uint64) *uint64 { return &value }

func TestBlockingFailedChecks(t *testing.T) {
	failedInitial := model.SafetyCheck{ID: 1, Kind: constants.CheckKindInitial, CheckCode: "INIT", Result: constants.CheckFailed}
	failedRecheck := model.SafetyCheck{ID: 2, Kind: constants.CheckKindRecheck, GroundUnitID: uint64Ptr(1), CheckCode: "RC-1", Result: constants.CheckFailed}
	passingRecheck := model.SafetyCheck{ID: 3, Kind: constants.CheckKindRecheck, GroundUnitID: uint64Ptr(1), CheckCode: "RC-2", Result: constants.CheckPassed}
	checks := []model.SafetyCheck{failedInitial, failedRecheck, passingRecheck}
	if got := blockingFailedChecks(checks, false); len(got) != 2 {
		t.Fatalf("outside recovery every failed check blocks, got %d", len(got))
	}
	blocked := blockingFailedChecks(checks, true)
	if len(blocked) != 1 || blocked[0].ID != 1 {
		t.Fatalf("during recovery only the failed initial check blocks, got %#v", blocked)
	}
}
