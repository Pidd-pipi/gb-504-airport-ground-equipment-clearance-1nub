package service

import (
	"strconv"

	"groundclearance/internal/constants"
	"groundclearance/internal/model"
)

// blockingFailedChecks returns the failed checks that still prevent a full
// clearance decision. Outside recovery every failed check blocks. During a
// revoked-clearance recovery, a failed recheck no longer blocks once a later
// recheck for the same ground unit supersedes it (the immutable failed record
// stays in the audit chain). Failed initial checks and rechecks whose latest
// conclusion per ground unit is still failed always block full clearance.
func blockingFailedChecks(checks []model.SafetyCheck, recovery bool) []model.SafetyCheck {
	if !recovery {
		failed := make([]model.SafetyCheck, 0)
		for _, check := range checks {
			if check.Result == constants.CheckFailed {
				failed = append(failed, check)
			}
		}
		return failed
	}
	latestRecheck := latestRecheckByUnit(checks)
	blocked := make([]model.SafetyCheck, 0)
	for _, check := range checks {
		if check.Result != constants.CheckFailed {
			continue
		}
		if check.Kind != constants.CheckKindRecheck {
			blocked = append(blocked, check)
			continue
		}
		key := uint64(0)
		if check.GroundUnitID != nil {
			key = *check.GroundUnitID
		}
		latest, exists := latestRecheck[key]
		if !exists || latest.ID == check.ID {
			blocked = append(blocked, check)
		}
	}
	return blocked
}

// latestRecheckByUnit returns the last recheck recorded per ground unit
// (key 0 covers rechecks without a targeted unit).
func latestRecheckByUnit(checks []model.SafetyCheck) map[uint64]model.SafetyCheck {
	latest := make(map[uint64]model.SafetyCheck)
	for _, check := range checks {
		if check.Kind != constants.CheckKindRecheck {
			continue
		}
		key := uint64(0)
		if check.GroundUnitID != nil {
			key = *check.GroundUnitID
		}
		latest[key] = check
	}
	return latest
}

// evaluateRecoveryGates returns human-readable blockers that prevent a safety
// manager from deciding again after a clearance was revoked because of faulty
// equipment. Recovery is allowed only when:
//   - the turnaround has returned to the checking stage;
//   - every assigned ground unit is available again;
//   - at least one recovery recheck exists;
//   - no recheck is still pending;
//   - the latest recheck recorded for each ground unit has passed (an earlier
//     failed recheck is retained for audit but can be superseded by a new
//     on-site recheck opened after further repair).
//
// It is shared by the clearance decision transaction and the read-only
// recovery endpoint so the desk and the server enforce the same red lines.
func evaluateRecoveryGates(turnaround *model.Turnaround, checks []model.SafetyCheck,
	findUnit func(uint64) (*model.GroundUnit, error)) []string {
	blockers := make([]string, 0)
	if turnaround.Status != constants.TurnaroundChecking {
		blockers = append(blockers, "turnaround must return to checking after revocation")
	}
	// Latest recheck conclusion per ground unit. Checks arrive ordered by
	// sequence, so later entries naturally overwrite earlier ones. Every
	// assigned ground unit must carry a recovery recheck; a recheck without a
	// targeted unit (key 0) is treated as an additional general recheck.
	latestByUnit := latestRecheckByUnit(checks)
	for _, check := range checks {
		if check.Kind == constants.CheckKindRecheck && check.Result == constants.CheckPending {
			blockers = append(blockers, "pending recovery recheck: "+check.CheckCode)
		}
	}
	for _, rawID := range turnaround.GroundUnitIDs {
		unitID, parseErr := strconv.ParseUint(rawID, 10, 64)
		if parseErr != nil || unitID == 0 {
			blockers = append(blockers, "turnaround contains an invalid ground unit")
			continue
		}
		recheck, hasRecheck := latestByUnit[unitID]
		unit, findErr := findUnit(unitID)
		if findErr != nil {
			blockers = append(blockers, "missing ground unit: "+rawID)
			continue
		}
		if !hasRecheck {
			blockers = append(blockers, "recovery recheck required for ground unit "+unit.UnitCode)
			continue
		}
		if recheck.Result == constants.CheckFailed {
			blockers = append(blockers, "latest recovery recheck failed for ground unit "+unit.UnitCode+": "+recheck.CheckCode)
		}
		if unit.State != constants.UnitAvailable {
			blockers = append(blockers, "ground unit "+unit.UnitCode+" is not restored (state: "+unit.State+")")
		}
	}
	for unitID, recheck := range latestByUnit {
		if unitID == 0 && recheck.Result == constants.CheckFailed {
			blockers = append(blockers, "latest recovery recheck failed: "+recheck.CheckCode)
		}
	}
	return blockers
}
