package service

import (
	"log/slog"
	"strconv"
	"testing"
	"time"

	"groundclearance/internal/constants"
	"groundclearance/internal/model"
	"groundclearance/internal/repository"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// recoveryFixture builds an isolated in-memory database with a single
// turnaround whose assigned ground unit has just been blocked, mirroring the
// equipment-fault auto-revocation: clearance is revoked and the turnaround is
// reopened to checking. Each test gets an isolated DSN.
type recoveryFixture struct {
	db             *gorm.DB
	unitSvc        *GroundUnitService
	checkSvc       *SafetyCheckService
	clearanceSvc   *ClearanceDecisionService
	turnaroundSvc  *TurnaroundService
	unit           model.GroundUnit
	turnaround     model.Turnaround
	managerActor   AuditContext
	inspectorActor AuditContext
}

func newRecoveryFixture(t *testing.T) *recoveryFixture {
	t.Helper()
	dsn := "file:" + strconv.FormatInt(time.Now().UnixNano(), 10) + "?mode=memory&cache=shared&_busy_timeout=5000"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.GroundUnit{},
		&model.SafetyCheck{}, &model.ClearanceDecision{}, &model.AuditLog{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// The turnaround model declares a PostgreSQL GIN index on its jsonb column
	// which SQLite cannot create. The recovery workflow never queries that
	// operator (only the blocking branch does), so an equivalent plain table is
	// sufficient here.
	if err := db.Exec(`CREATE TABLE turnarounds (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		flight_no TEXT NOT NULL,
		stand TEXT NOT NULL,
		phase TEXT NOT NULL,
		scheduled_at DATETIME,
		risk_level TEXT NOT NULL DEFAULT 'medium',
		status TEXT NOT NULL DEFAULT 'open',
		ground_unit_ids TEXT,
		coordinator_id INTEGER NOT NULL,
		version INTEGER NOT NULL DEFAULT 1,
		created_at DATETIME,
		updated_at DATETIME
	)`).Error; err != nil {
		t.Fatalf("create turnarounds table: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(&testWriter{t: t}, nil))
	unitRepo := repository.NewGroundUnitRepository(db)
	turnaroundRepo := repository.NewTurnaroundRepository(db)
	checkRepo := repository.NewSafetyCheckRepository(db)
	clearanceRepo := repository.NewClearanceDecisionRepository(db)
	userRepo := repository.NewUserRepository(db)

	fixture := &recoveryFixture{
		db: db,
		unitSvc:        NewGroundUnitService(db, unitRepo, turnaroundRepo, clearanceRepo, logger),
		turnaroundSvc:  NewTurnaroundService(db, turnaroundRepo, checkRepo, clearanceRepo, unitRepo, userRepo, logger),
		checkSvc:       NewSafetyCheckService(db, checkRepo, turnaroundRepo, clearanceRepo, unitRepo, logger),
		clearanceSvc:   NewClearanceDecisionService(db, clearanceRepo, turnaroundRepo, checkRepo, unitRepo, logger),
		managerActor:   AuditContext{OperatorID: 1, OperatorName: "13800000002", RequestID: "req-manager", IP: "127.0.0.1"},
		inspectorActor: AuditContext{OperatorID: 2, OperatorName: "13800000003", RequestID: "req-inspector", IP: "127.0.0.1"},
	}

	manager := model.User{Phone: "13800000002", PasswordHash: "x", Name: "manager", Role: constants.RoleSafetyManager}
	inspector := model.User{Phone: "13800000003", PasswordHash: "x", Name: "inspector", Role: constants.RoleInspector}
	if err := db.Create(&manager).Error; err != nil {
		t.Fatalf("create manager: %v", err)
	}
	if err := db.Create(&inspector).Error; err != nil {
		t.Fatalf("create inspector: %v", err)
	}
	unit := model.GroundUnit{UnitCode: "BLT-001", Name: "belt loader", UnitType: "belt_loader", Stand: "A1", State: constants.UnitAvailable}
	if err := db.Create(&unit).Error; err != nil {
		t.Fatalf("create unit: %v", err)
	}
	turnaround := model.Turnaround{
		FlightNo: "CA1", Stand: "A1", Phase: "servicing", ScheduledAt: time.Now().Add(time.Hour),
		RiskLevel: constants.RiskHigh, Status: constants.TurnaroundDecisioned,
		GroundUnitIDs: model.JSONList{strconv.FormatUint(unit.ID, 10)}, CoordinatorID: manager.ID,
	}
	if err := db.Create(&turnaround).Error; err != nil {
		t.Fatalf("create turnaround: %v", err)
	}
	now := time.Now()
	check := model.SafetyCheck{TurnaroundID: turnaround.ID, GroundUnitID: &unit.ID, Sequence: 1, Kind: constants.CheckKindInitial,
		CheckCode: "INIT-1", ItemName: "initial stop check", RiskLevel: constants.RiskHigh, Result: constants.CheckPassed,
		Evidence: model.JSONList{"init.jpg"}, CheckedBy: inspector.ID, CheckedAt: &now}
	if err := db.Create(&check).Error; err != nil {
		t.Fatalf("create check: %v", err)
	}
	decision := model.ClearanceDecision{TurnaroundID: turnaround.ID, State: constants.ClearanceCleared,
		PreviousState: constants.ClearancePending, Reason: "initial clearance", Evidence: model.JSONList{"mgr.jpg"},
		OperatorID: manager.ID, DecidedAt: now}
	if err := db.Create(&decision).Error; err != nil {
		t.Fatalf("create decision: %v", err)
	}

	// Simulate the persisted result of GroundUnitService.ChangeState when the
	// assigned equipment is reported faulty: the unit is blocked, the clearance
	// auto-transitions cleared -> revoked and the turnaround reopens
	// decisioned -> checking in the same workflow. (That service path uses
	// PostgreSQL jsonb operators, so it is exercised against Postgres rather
	// than the SQLite test database.)
	unit.State = constants.UnitBlocked
	unit.Notes = "emergency stop failed"
	unit.Version++
	if err := db.Save(&unit).Error; err != nil {
		t.Fatalf("block unit: %v", err)
	}
	revokedAt := time.Now()
	decision.PreviousState = constants.ClearanceCleared
	decision.State = constants.ClearanceRevoked
	decision.Reason = "assigned equipment BLT-001 changed to blocked: emergency stop failed"
	decision.Evidence = model.JSONList{"mgr.jpg", "ground-unit:BLT-001"}
	decision.OperatorID = inspector.ID
	decision.DecidedAt = revokedAt
	if err := db.Save(&decision).Error; err != nil {
		t.Fatalf("revoke decision: %v", err)
	}
	turnaround.Status = constants.TurnaroundChecking
	turnaround.Version++
	if err := db.Save(&turnaround).Error; err != nil {
		t.Fatalf("reopen turnaround: %v", err)
	}

	if err := db.First(&turnaround, turnaround.ID).Error; err != nil {
		t.Fatalf("reload turnaround: %v", err)
	}
	if turnaround.Status != constants.TurnaroundChecking {
		t.Fatalf("turnaround must return to checking after revocation, got %s", turnaround.Status)
	}
	var revokedDecision model.ClearanceDecision
	if err := db.Where("turnaround_id = ?", turnaround.ID).First(&revokedDecision).Error; err != nil {
		t.Fatalf("reload decision: %v", err)
	}
	if revokedDecision.State != constants.ClearanceRevoked {
		t.Fatalf("decision must be revoked, got %s", revokedDecision.State)
	}
	fixture.unit = unit
	fixture.turnaround = turnaround
	return fixture
}

type testWriter struct{ t *testing.T }

func (w *testWriter) Write(p []byte) (int, error) { w.t.Logf("%s", p); return len(p), nil }

func (f *recoveryFixture) reopenRecheck(t *testing.T, code string) *model.SafetyCheck {
	t.Helper()
	unitID := f.unit.ID
	recheck := &model.SafetyCheck{TurnaroundID: f.turnaround.ID, GroundUnitID: &unitID,
		CheckCode: code, ItemName: "emergency stop recheck", RiskLevel: constants.RiskHigh,
		Evidence: model.JSONList{"rc-onsite.jpg"}}
	recheck, err := f.checkSvc.CreateRecheck(recheck, f.inspectorActor)
	if err != nil {
		t.Fatalf("create recheck %s: %v", code, err)
	}
	return recheck
}

func (f *recoveryFixture) restoreUnit(t *testing.T) {
	t.Helper()
	var unit model.GroundUnit
	if err := f.db.First(&unit, f.unit.ID).Error; err != nil {
		t.Fatalf("reload unit: %v", err)
	}
	if _, err := f.unitSvc.ChangeState(unit.ID, constants.UnitAvailable, "repair completed and tested",
		unit.Version, constants.RoleSafetyManager, f.managerActor); err != nil {
		t.Fatalf("restore unit: %v", err)
	}
}

func (f *recoveryFixture) decideMustFail(t *testing.T, label string) {
	t.Helper()
	if _, err := f.clearanceSvc.Decide(f.turnaround.ID, 1, constants.ClearanceCleared, "", label,
		[]string{"mgr.jpg"}, "req-decide", "mgr", "127.0.0.1"); err == nil {
		t.Fatalf("%s: re-decision must be rejected", label)
	}
}

func TestRecoveryWorkflowEndToEnd(t *testing.T) {
	f := newRecoveryFixture(t)

	// Before any recheck exists the safety manager cannot decide again.
	f.decideMustFail(t, "no recheck")

	// A pending recheck keeps the decision blocked even after repair.
	first := f.reopenRecheck(t, "RC-1")
	f.restoreUnit(t)
	f.decideMustFail(t, "pending recheck")

	// A failed recheck blocks, even with the equipment already restored.
	if _, err := f.checkSvc.Review(first.ID, 2, constants.CheckFailed, []string{"rc-failed.jpg"}, "still loose",
		f.inspectorActor); err != nil {
		t.Fatalf("review failed recheck: %v", err)
	}
	f.decideMustFail(t, "failed recheck")

	// Opening a new recheck after further repair and passing it supersedes the
	// failed one; the immutable failed record stays in the audit chain but no
	// longer blocks recovery.
	second := f.reopenRecheck(t, "RC-2")
	if _, err := f.checkSvc.Review(second.ID, 2, constants.CheckPassed, []string{"rc-passed.jpg"}, "fixed",
		f.inspectorActor); err != nil {
		t.Fatalf("review passing recheck: %v", err)
	}

	detail, err := f.turnaroundSvc.RecoveryDetail(f.turnaround.ID)
	if err != nil {
		t.Fatalf("recovery detail: %v", err)
	}
	if detail["revocation_reason"] == "" {
		t.Fatal("revocation reason must be exposed")
	}
	progress, _ := detail["recheck_progress"].(map[string]any)
	if progress["total"].(int) != 2 || progress["failed"].(int) != 1 || progress["passed"].(int) != 1 {
		t.Fatalf("unexpected recheck progress: %#v", progress)
	}
	if len(detail["units"].([]map[string]any)) != 1 {
		t.Fatalf("recovery detail must include assigned unit states: %#v", detail["units"])
	}

	decision, err := f.clearanceSvc.Decide(f.turnaround.ID, 1, constants.ClearanceCleared, "", "repair verified",
		[]string{"mgr-final.jpg"}, "req-final", "mgr", "127.0.0.1")
	if err != nil {
		t.Fatalf("re-decision after full recovery must succeed: %v", err)
	}
	if decision.State != constants.ClearanceCleared || decision.PreviousState != constants.ClearanceRevoked {
		t.Fatalf("unexpected decision: state=%s previous=%s", decision.State, decision.PreviousState)
	}
	var row model.Turnaround
	if err := f.db.First(&row, f.turnaround.ID).Error; err != nil {
		t.Fatalf("reload turnaround: %v", err)
	}
	if row.Status != constants.TurnaroundDecisioned {
		t.Fatalf("turnaround must be decisioned again, got %s", row.Status)
	}
}

func TestRecoveryBlockedWhenEquipmentNotRestored(t *testing.T) {
	f := newRecoveryFixture(t)
	recheck := f.reopenRecheck(t, "RC-OK")
	if _, err := f.checkSvc.Review(recheck.ID, 2, constants.CheckPassed, []string{"rc-pass.jpg"}, "fixed",
		f.inspectorActor); err != nil {
		t.Fatalf("review recheck: %v", err)
	}
	// Recheck passed but the unit is still blocked: the decision is refused.
	if _, err := f.clearanceSvc.Decide(f.turnaround.ID, 1, constants.ClearanceRestricted, "monitor", "recheck passed but unit still blocked",
		[]string{"mgr.jpg"}, "req-blocked-unit", "mgr", "127.0.0.1"); err == nil {
		t.Fatal("re-decision must be rejected while the equipment is not restored")
	}

	detail, err := f.turnaroundSvc.RecoveryDetail(f.turnaround.ID)
	if err != nil {
		t.Fatalf("recovery detail: %v", err)
	}
	if detail["recovery_complete"].(bool) {
		t.Fatal("recovery must not be complete with a blocked unit")
	}
	blockers, _ := detail["recovery_blockers"].([]string)
	if len(blockers) != 1 || blockers[0] == "" {
		t.Fatalf("expected exactly the unrestored-unit blocker, got: %v", blockers)
	}
}

func TestRecheckCanFollowEquipmentRepair(t *testing.T) {
	f := newRecoveryFixture(t)
	// Equipment is repaired first; the inspector must still be able to open a
	// recheck afterwards (the original bug left no recheck item to complete).
	f.restoreUnit(t)
	recheck := f.reopenRecheck(t, "RC-AFTER")
	if _, err := f.checkSvc.Review(recheck.ID, 2, constants.CheckPassed, []string{"rc-after.jpg"}, "verified post-repair",
		f.inspectorActor); err != nil {
		t.Fatalf("review post-repair recheck: %v", err)
	}
	if _, err := f.clearanceSvc.Decide(f.turnaround.ID, 1, constants.ClearanceCleared, "", "post-repair verified",
		[]string{"mgr.jpg"}, "req-after", "mgr", "127.0.0.1"); err != nil {
		t.Fatalf("recovery must succeed after repair + passing recheck: %v", err)
	}
}

func TestManualEmergencyRevocationReopensTurnaround(t *testing.T) {
	f := newRecoveryFixture(t)
	// Complete the recovery once, then revoke again manually and verify the
	// turnaround re-enters checking and a fresh recovery is required.
	recheck := f.reopenRecheck(t, "RC-MANUAL")
	f.restoreUnit(t)
	if _, err := f.checkSvc.Review(recheck.ID, 2, constants.CheckPassed, []string{"rc.jpg"}, "ok",
		f.inspectorActor); err != nil {
		t.Fatalf("review recheck: %v", err)
	}
	if _, err := f.clearanceSvc.Decide(f.turnaround.ID, 1, constants.ClearanceCleared, "", "recovered",
		[]string{"mgr.jpg"}, "req-recovered", "mgr", "127.0.0.1"); err != nil {
		t.Fatalf("recovery decision: %v", err)
	}
	if _, err := f.clearanceSvc.Decide(f.turnaround.ID, 1, constants.ClearanceRevoked, "", "new on-site emergency",
		[]string{"emergency.jpg"}, "req-revoke", "mgr", "127.0.0.1"); err != nil {
		t.Fatalf("emergency revocation: %v", err)
	}
	var row model.Turnaround
	if err := f.db.First(&row, f.turnaround.ID).Error; err != nil {
		t.Fatalf("reload turnaround: %v", err)
	}
	if row.Status != constants.TurnaroundChecking {
		t.Fatalf("manual revocation must reopen turnaround to checking, got %s", row.Status)
	}
	var decision model.ClearanceDecision
	if err := f.db.Where("turnaround_id = ?", f.turnaround.ID).First(&decision).Error; err != nil {
		t.Fatalf("reload decision: %v", err)
	}
	if decision.State != constants.ClearanceRevoked || decision.Reason != "new on-site emergency" {
		t.Fatalf("decision must be manually revoked with the new reason, got %s/%s", decision.State, decision.Reason)
	}
}
