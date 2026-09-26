package service

import (
	"encoding/json"
	"log/slog"
	"strconv"
	"time"

	"groundclearance/internal/constants"
	"groundclearance/internal/model"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

type AuditContext struct {
	OperatorID   uint64
	OperatorName string
	RequestID    string
	IP           string
}

func persistTransitionAudit(tx *gorm.DB, actor AuditContext, action, entityType string, entityID uint64, detail map[string]any) error {
	detail["request_id"] = actor.RequestID
	raw, err := json.Marshal(detail)
	if err != nil {
		return err
	}
	return tx.Create(&model.AuditLog{
		OperatorID: actor.OperatorID, OperatorName: actor.OperatorName, Action: action,
		EntityType: entityType, EntityID: strconv.FormatUint(entityID, 10), Detail: string(raw),
		IP: actor.IP, CreatedAt: time.Now(),
	}).Error
}

// SeedService creates a coherent demo shift on an empty database.
type SeedService struct {
	db     *gorm.DB
	logger *slog.Logger
}

func NewSeedService(db *gorm.DB, logger *slog.Logger) *SeedService {
	return &SeedService{db: db, logger: logger}
}

func (s *SeedService) Seed() error {
	var count int64
	if err := s.db.Model(&model.User{}).Count(&count).Error; err != nil || count > 0 {
		return err
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		adminHash, _ := bcrypt.GenerateFromPassword([]byte("Admin@123"), bcrypt.DefaultCost)
		userHash, _ := bcrypt.GenerateFromPassword([]byte("User@123"), bcrypt.DefaultCost)
		users := []model.User{
			{Phone: "13800000001", PasswordHash: string(adminHash), Name: "林调度", Role: constants.RoleAdmin},
			{Phone: "13800000002", PasswordHash: string(userHash), Name: "周安全", Role: constants.RoleSafetyManager},
			{Phone: "13800000003", PasswordHash: string(userHash), Name: "陈检查", Role: constants.RoleInspector},
			{Phone: "13800000004", PasswordHash: string(userHash), Name: "赵地勤", Role: constants.RoleWorker},
		}
		if err := tx.Create(&users).Error; err != nil {
			return err
		}
		now := time.Now()
		inspected := now.Add(-2 * time.Hour)
		units := []model.GroundUnit{
			{UnitCode: "TUG-017", Name: "飞机牵引车 17", UnitType: "tug", Stand: "A12", State: constants.UnitAvailable, LastInspectionAt: &inspected, Notes: "班前检查正常"},
			{UnitCode: "GPU-204", Name: "地面电源车 204", UnitType: "gpu", Stand: "A12", State: constants.UnitInspection, LastInspectionAt: &inspected, Notes: "等待绝缘测试"},
			{UnitCode: "BLT-088", Name: "行李传送带 88", UnitType: "belt_loader", Stand: "B06", State: constants.UnitBlocked, LastInspectionAt: &inspected, Notes: "急停开关异常"},
			{UnitCode: "WTR-031", Name: "清水车 31", UnitType: "water_service", Stand: "B06", State: constants.UnitAvailable, LastInspectionAt: &inspected},
		}
		if err := tx.Create(&units).Error; err != nil {
			return err
		}
		turnarounds := []model.Turnaround{
			{FlightNo: "CA1831", Stand: "A12", Phase: "servicing", ScheduledAt: now.Add(35 * time.Minute), RiskLevel: constants.RiskHigh, Status: constants.TurnaroundChecking, GroundUnitIDs: model.JSONList{"1", "2"}, CoordinatorID: users[1].ID},
			{FlightNo: "MU2458", Stand: "B06", Phase: "departure", ScheduledAt: now.Add(90 * time.Minute), RiskLevel: constants.RiskMedium, Status: constants.TurnaroundOpen, GroundUnitIDs: model.JSONList{"4"}, CoordinatorID: users[1].ID},
			// Equipment BLT-088 failed after full clearance: the decision was
			// revoked and the turnaround was reopened so inspectors can open a
			// recovery recheck before the safety manager can decide again.
			{FlightNo: "CZ8621", Stand: "B06", Phase: "departure", ScheduledAt: now.Add(110 * time.Minute), RiskLevel: constants.RiskHigh, Status: constants.TurnaroundChecking, GroundUnitIDs: model.JSONList{"3"}, CoordinatorID: users[1].ID},
		}
		if err := tx.Create(&turnarounds).Error; err != nil {
			return err
		}
		checks := []model.SafetyCheck{
			{TurnaroundID: turnarounds[0].ID, GroundUnitID: &units[0].ID, Sequence: 1, Kind: constants.CheckKindInitial, CheckCode: "TUG-BRAKE", ItemName: "牵引车制动与转向", RiskLevel: constants.RiskHigh, Result: constants.CheckPassed, Evidence: model.JSONList{"brake-test-20260822.jpg"}, CheckedBy: users[2].ID, CheckedAt: &now},
			{TurnaroundID: turnarounds[0].ID, GroundUnitID: &units[1].ID, Sequence: 2, Kind: constants.CheckKindInitial, CheckCode: "GPU-INSULATION", ItemName: "地面电源绝缘及接地", RiskLevel: constants.RiskCritical, Result: constants.CheckPending},
			{TurnaroundID: turnarounds[1].ID, GroundUnitID: &units[3].ID, Sequence: 1, Kind: constants.CheckKindInitial, CheckCode: "WTR-SEAL", ItemName: "水管密封与停车制动", RiskLevel: constants.RiskMedium, Result: constants.CheckPending},
			{TurnaroundID: turnarounds[2].ID, GroundUnitID: &units[2].ID, Sequence: 1, Kind: constants.CheckKindInitial, CheckCode: "BLT-STOP", ItemName: "传送带急停与制动", RiskLevel: constants.RiskHigh, Result: constants.CheckPassed, Evidence: model.JSONList{"blt-stop-20260822.jpg"}, CheckedBy: users[2].ID, CheckedAt: &now},
		}
		if err := tx.Create(&checks).Error; err != nil {
			return err
		}
		revokedAt := now.Add(-15 * time.Minute)
		decisions := []model.ClearanceDecision{
			{TurnaroundID: turnarounds[0].ID, State: constants.ClearancePending, Reason: "awaiting checks"},
			{TurnaroundID: turnarounds[1].ID, State: constants.ClearancePending, Reason: "awaiting checks"},
			{TurnaroundID: turnarounds[2].ID, State: constants.ClearanceRevoked, PreviousState: constants.ClearanceCleared,
				Reason:    "assigned equipment BLT-088 changed to blocked: 急停开关异常",
				Evidence:  model.JSONList{"blt-stop-fault-20260822.jpg", "ground-unit:BLT-088"},
				OperatorID: users[2].ID, DecidedAt: revokedAt},
		}
		if err := tx.Create(&decisions).Error; err != nil {
			return err
		}
		s.logger.Info("airport ground clearance seed data created")
		return nil
	})
}
