package service

import (
	"errors"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

	"groundclearance/internal/constants"
	"groundclearance/internal/model"
	"groundclearance/internal/repository"
	"groundclearance/internal/util"

	"gorm.io/gorm"
)

type TurnaroundService struct {
	db            *gorm.DB
	repo          *repository.TurnaroundRepository
	checkRepo     *repository.SafetyCheckRepository
	clearanceRepo *repository.ClearanceDecisionRepository
	unitRepo      *repository.GroundUnitRepository
	userRepo      *repository.UserRepository
	logger        *slog.Logger
}

func NewTurnaroundService(db *gorm.DB, repo *repository.TurnaroundRepository,
	checkRepo *repository.SafetyCheckRepository, clearanceRepo *repository.ClearanceDecisionRepository,
	unitRepo *repository.GroundUnitRepository, userRepo *repository.UserRepository, logger *slog.Logger) *TurnaroundService {
	return &TurnaroundService{db: db, repo: repo, checkRepo: checkRepo, clearanceRepo: clearanceRepo, unitRepo: unitRepo, userRepo: userRepo, logger: logger}
}

func (s *TurnaroundService) Create(row *model.Turnaround, checks []model.SafetyCheck, actor AuditContext) (*model.Turnaround, error) {
	row.FlightNo = strings.ToUpper(strings.TrimSpace(row.FlightNo))
	row.Stand = strings.ToUpper(strings.TrimSpace(row.Stand))
	row.Phase = strings.TrimSpace(row.Phase)
	if row.FlightNo == "" || row.Stand == "" || row.Phase == "" || row.ScheduledAt.IsZero() {
		return nil, util.NewAppError(constants.CodeValidationFailed, "flight, stand, phase and schedule are required")
	}
	if !constants.IsValidRiskLevel(row.RiskLevel) {
		return nil, util.NewAppError(constants.CodeValidationFailed, "invalid risk level")
	}
	coordinator, err := s.userRepo.FindByID(row.CoordinatorID)
	if err != nil {
		return nil, util.NewAppError(constants.CodeValidationFailed, "coordinator does not exist")
	}
	if coordinator.Role != constants.RoleAdmin && coordinator.Role != constants.RoleSafetyManager {
		return nil, util.NewAppError(constants.CodeValidationFailed, "coordinator must be an authorized safety manager")
	}
	if len(row.GroundUnitIDs) == 0 {
		return nil, util.NewAppError(constants.CodeValidationFailed, "at least one ground unit is required")
	}
	if len(checks) == 0 {
		return nil, util.NewAppError(constants.CodeValidationFailed, "at least one safety check is required")
	}
	assignedUnits := make(map[uint64]struct{}, len(row.GroundUnitIDs))
	unitIDs := make([]uint64, 0, len(row.GroundUnitIDs))
	for _, rawID := range row.GroundUnitIDs {
		id, err := strconv.ParseUint(rawID, 10, 64)
		if err != nil || id == 0 {
			return nil, util.NewAppError(constants.CodeValidationFailed, "invalid ground unit id")
		}
		if _, duplicate := assignedUnits[id]; duplicate {
			return nil, util.NewAppError(constants.CodeValidationFailed, "ground unit ids must be unique")
		}
		assignedUnits[id] = struct{}{}
		unitIDs = append(unitIDs, id)
	}
	sort.Slice(unitIDs, func(i, j int) bool { return unitIDs[i] < unitIDs[j] })
	checkCodes := make(map[string]struct{}, len(checks))
	for index := range checks {
		checks[index].CheckCode = strings.ToUpper(strings.TrimSpace(checks[index].CheckCode))
		checks[index].ItemName = strings.TrimSpace(checks[index].ItemName)
		if checks[index].CheckCode == "" || checks[index].ItemName == "" || !constants.IsValidRiskLevel(checks[index].RiskLevel) {
			return nil, util.NewAppError(constants.CodeValidationFailed, "invalid safety check definition")
		}
		if _, duplicate := checkCodes[checks[index].CheckCode]; duplicate {
			return nil, util.NewAppError(constants.CodeValidationFailed, "check codes must be unique within a turnaround")
		}
		checkCodes[checks[index].CheckCode] = struct{}{}
		if checks[index].GroundUnitID != nil {
			if _, assigned := assignedUnits[*checks[index].GroundUnitID]; !assigned {
				return nil, util.NewAppError(constants.CodeValidationFailed, "safety check equipment must be assigned to the turnaround")
			}
		}
	}
	row.Status = constants.TurnaroundOpen
	err = s.db.Transaction(func(tx *gorm.DB) error {
		for _, unitID := range unitIDs {
			if err := s.unitRepo.LockAssignmentTx(tx, unitID); err != nil {
				return err
			}
		}
		seenTurnarounds := make(map[uint64]struct{})
		for _, unitID := range unitIDs {
			active, err := s.repo.FindActiveByGroundUnitTx(tx, unitID)
			if err != nil {
				return err
			}
			for _, existing := range active {
				if _, seen := seenTurnarounds[existing.ID]; seen {
					continue
				}
				seenTurnarounds[existing.ID] = struct{}{}
				if existing.Status == constants.TurnaroundOpen || existing.Status == constants.TurnaroundChecking {
					return util.NewAppError(constants.CodeStateConflict, "ground unit is assigned to an active turnaround")
				}
				decision, err := s.clearanceRepo.FindByTurnaroundTx(tx, existing.ID)
				if err != nil || decision.State != constants.ClearanceRevoked {
					return util.NewAppError(constants.CodeStateConflict, "ground unit is assigned to an active turnaround")
				}
			}
		}
		for _, unitID := range unitIDs {
			unit, err := s.unitRepo.FindByIDTx(tx, unitID)
			if err != nil || unit.State != constants.UnitAvailable {
				return util.NewAppError(constants.CodeValidationFailed, "all assigned ground units must be available")
			}
		}
		if err := s.repo.CreateTx(tx, row); err != nil {
			return err
		}
		for index := range checks {
			checks[index].TurnaroundID = row.ID
			checks[index].Sequence = index + 1
			checks[index].Kind = constants.CheckKindInitial
			checks[index].Result = constants.CheckPending
		}
		if err := s.checkRepo.CreateManyTx(tx, checks); err != nil {
			return err
		}
		decision := &model.ClearanceDecision{TurnaroundID: row.ID, State: constants.ClearancePending, Reason: "awaiting checks"}
		if err := s.clearanceRepo.SaveTx(tx, decision); err != nil {
			return err
		}
		return persistTransitionAudit(tx, actor, "TURNAROUND_CREATED", "turnarounds", row.ID, map[string]any{
			"flight_no": row.FlightNo, "stand": row.Stand, "ground_unit_ids": row.GroundUnitIDs,
			"check_count": len(checks), "clearance_id": decision.ID,
		})
	})
	if err != nil {
		return nil, util.Wrap(err, "Turnaround[flight_no=%s] create failed", row.FlightNo)
	}
	s.logger.Info(constants.LogTurnaroundCreated, "turnaround_id", row.ID, "flight_no", row.FlightNo)
	return row, nil
}

func (s *TurnaroundService) List(page, pageSize int, status, risk, search string) ([]model.Turnaround, int64, error) {
	status = strings.TrimSpace(status)
	risk = strings.TrimSpace(risk)
	search = strings.TrimSpace(search)
	if status != "" && !constants.In(constants.TurnaroundStatusValues, status) {
		return nil, 0, util.NewAppError(constants.CodeValidationFailed, "invalid turnaround status filter")
	}
	if risk != "" && !constants.IsValidRiskLevel(risk) {
		return nil, 0, util.NewAppError(constants.CodeValidationFailed, "invalid risk filter")
	}
	if len(search) > 100 {
		return nil, 0, util.NewAppError(constants.CodeValidationFailed, "search value is too long")
	}
	return s.repo.List(page, pageSize, status, risk, search)
}

func (s *TurnaroundService) Summary() (map[string]any, error) {
	return s.repo.Summary(time.Now())
}

func (s *TurnaroundService) Get(id uint64) (*model.Turnaround, []model.SafetyCheck, error) {
	row, err := s.repo.FindByID(id)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, nil, util.NewAppError(constants.CodeNotFound, constants.MsgNotFound)
		}
		return nil, nil, err
	}
	checks, err := s.checkRepo.ListByTurnaround(id)
	return row, checks, err
}

func (s *TurnaroundService) ChangeStatus(id uint64, status string, version int, actor AuditContext) (*model.Turnaround, error) {
	if !constants.In(constants.TurnaroundStatusValues, status) {
		return nil, util.NewAppError(constants.CodeValidationFailed, "invalid turnaround status")
	}
	var row *model.Turnaround
	err := s.db.Transaction(func(tx *gorm.DB) error {
		locked, err := s.repo.FindByIDTx(tx, id)
		if err != nil {
			if errors.Is(err, repository.ErrNotFound) {
				return util.NewAppError(constants.CodeNotFound, constants.MsgNotFound)
			}
			return err
		}
		if locked.Version != version {
			return util.NewAppError(constants.CodeConflict, "turnaround was updated by another operator")
		}
		if !allowedTurnaroundTransition(locked.Status, status) {
			return util.NewAppError(constants.CodeStateConflict, "turnaround status transition is not allowed")
		}
		previous := locked.Status
		if status == constants.TurnaroundCompleted {
			decision, err := s.clearanceRepo.FindByTurnaroundTx(tx, id)
			if err != nil {
				return err
			}
			if decision.State != constants.ClearanceCleared && decision.State != constants.ClearanceRestricted {
				return util.NewAppError(constants.CodeStateConflict, "only cleared or restricted turnarounds can be completed")
			}
		}
		locked.Status = status
		if err := s.repo.UpdateStatusTx(tx, locked, version); err != nil {
			if errors.Is(err, repository.ErrConflict) {
				return util.NewAppError(constants.CodeConflict, "turnaround was updated by another operator")
			}
			return err
		}
		if err := persistTransitionAudit(tx, actor, "TURNAROUND_STATUS_TRANSITION", "turnarounds", locked.ID, map[string]any{
			"previous_status": previous, "status": status, "version": locked.Version,
		}); err != nil {
			return err
		}
		row = locked
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.logger.Info(constants.LogTurnaroundUpdated, "turnaround_id", row.ID, "status", status)
	return row, nil
}

func allowedTurnaroundTransition(from, to string) bool {
	return (from == constants.TurnaroundOpen && to == constants.TurnaroundChecking) ||
		(from == constants.TurnaroundDecisioned && to == constants.TurnaroundCompleted)
}

// RecoveryDetail assembles the read model for the post-revocation recovery
// workflow: the revocation reason, the current state of every assigned unit,
// the recovery recheck progress and the blockers that still prevent a new
// clearance decision. Ground workers use the same read-only view.
func (s *TurnaroundService) RecoveryDetail(id uint64) (map[string]any, error) {
	row, err := s.repo.FindByID(id)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, util.NewAppError(constants.CodeNotFound, constants.MsgNotFound)
		}
		return nil, err
	}
	checks, err := s.checkRepo.ListByTurnaround(id)
	if err != nil {
		return nil, err
	}
	decision, err := s.clearanceRepo.FindByTurnaround(id)
	if err != nil {
		return nil, err
	}
	units := make([]map[string]any, 0, len(row.GroundUnitIDs))
	rechecks := make([]model.SafetyCheck, 0)
	for _, check := range checks {
		if check.Kind == constants.CheckKindRecheck {
			rechecks = append(rechecks, check)
		}
	}
	blockers := make([]string, 0)
	if decision.State != constants.ClearanceRevoked {
		blockers = append(blockers, "clearance is not revoked")
	}
	findUnit := func(unitID uint64) (*model.GroundUnit, error) { return s.unitRepo.FindByID(unitID) }
	blockers = append(blockers, evaluateRecoveryGates(row, checks, findUnit)...)
	for _, rawID := range row.GroundUnitIDs {
		unitID, parseErr := strconv.ParseUint(rawID, 10, 64)
		entry := map[string]any{"id": rawID, "state": "unknown", "unit_code": "", "name": ""}
		if parseErr == nil && unitID > 0 {
			if unit, findErr := s.unitRepo.FindByID(unitID); findErr == nil {
				entry["id"] = unit.ID
				entry["state"] = unit.State
				entry["unit_code"] = unit.UnitCode
				entry["name"] = unit.Name
				entry["notes"] = unit.Notes
			}
		}
		units = append(units, entry)
	}
	recheckPending, recheckPassed, recheckFailed := 0, 0, 0
	for _, recheck := range rechecks {
		switch recheck.Result {
		case constants.CheckPending:
			recheckPending++
		case constants.CheckPassed:
			recheckPassed++
		case constants.CheckFailed:
			recheckFailed++
		}
	}
	return map[string]any{
		"turnaround": row, "clearance": decision, "units": units, "rechecks": rechecks,
		"recheck_progress": map[string]any{
			"total": len(rechecks), "pending": recheckPending, "passed": recheckPassed, "failed": recheckFailed,
		},
		"revoked":            decision.State == constants.ClearanceRevoked,
		"revocation_reason":  decision.Reason,
		"recovery_blockers":  blockers,
		"recovery_complete":  decision.State == constants.ClearanceRevoked && len(blockers) == 0,
	}, nil
}

// Readiness evaluates all persisted blockers for a single turnaround. It is a
// read model used by both the inspection desk and the clearance review.
func (s *TurnaroundService) Readiness(id uint64) (map[string]any, error) {
	row, checks, err := s.Get(id)
	if err != nil {
		return nil, err
	}
	blockers := make([]string, 0)
	pending := 0
	failed := 0
	for _, check := range checks {
		switch check.Result {
		case constants.CheckPending:
			pending++
			blockers = append(blockers, "pending check: "+check.CheckCode)
		case constants.CheckFailed:
			failed++
		}
	}
	decision, decisionErr := s.clearanceRepo.FindByTurnaround(id)
	clearanceState := constants.ClearancePending
	if decisionErr == nil {
		clearanceState = decision.State
	}
	// A failed recheck superseded by a later passing recheck for the same unit
	// stays in the ledger but no longer blocks the read model during recovery.
	for _, check := range blockingFailedChecks(checks, clearanceState == constants.ClearanceRevoked) {
		blockers = append(blockers, "failed check: "+check.CheckCode)
	}
	unitStates := make(map[string]string, len(row.GroundUnitIDs))
	for _, rawID := range row.GroundUnitIDs {
		unitID, parseErr := strconv.ParseUint(rawID, 10, 64)
		if parseErr != nil {
			blockers = append(blockers, "invalid ground unit id: "+rawID)
			continue
		}
		unit, findErr := s.unitRepo.FindByID(unitID)
		if findErr != nil {
			blockers = append(blockers, "missing ground unit: "+rawID)
			continue
		}
		unitStates[rawID] = unit.State
		if unit.State != constants.UnitAvailable {
			blockers = append(blockers, "ground unit "+unit.UnitCode+" is "+unit.State)
		}
	}
	readyForDecision := pending == 0
	readyForFullClearance := pending == 0 && failed == 0 && len(blockers) == 0
	return map[string]any{
		"turnaround_id": id, "flight_no": row.FlightNo, "status": row.Status,
		"pending_checks": pending, "failed_checks": failed, "unit_states": unitStates,
		"clearance_state": clearanceState, "ready_for_decision": readyForDecision,
		"ready_for_full_clearance": readyForFullClearance, "blockers": blockers,
	}, nil
}
