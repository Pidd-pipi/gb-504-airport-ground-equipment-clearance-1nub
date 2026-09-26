import { CommonModule } from '@angular/common';
import { Component, EventEmitter, inject, Input, Output } from '@angular/core';
import { FormBuilder, ReactiveFormsModule, Validators } from '@angular/forms';
import { MatButtonModule } from '@angular/material/button';
import { MatFormFieldModule } from '@angular/material/form-field';
import { MatIconModule } from '@angular/material/icon';
import { MatInputModule } from '@angular/material/input';
import { MatSelectModule } from '@angular/material/select';
import { MatSnackBar, MatSnackBarModule } from '@angular/material/snack-bar';
import { checkCreateApi } from '../../api/check.api';
import { CHECK_KIND_TEXT, RISK_TEXT, STATUS_TEXT } from '../../constants/enums';
import { RecoveryDetail } from '../../types';
import { parseHttpError, useHttp } from '../../utils/request';
import { StatusBadgeComponent } from './status-badge.component';

@Component({
  selector: 'app-recovery-panel',
  standalone: true,
  imports: [
    CommonModule, ReactiveFormsModule, MatButtonModule, MatFormFieldModule, MatIconModule, MatInputModule,
    MatSelectModule, MatSnackBarModule, StatusBadgeComponent,
  ],
  template: `
    <section class="recovery-panel" *ngIf="detail">
      <header>
        <span><mat-icon>build_circle</mat-icon>撤销恢复流程</span>
        <app-status-badge value="revoked"></app-status-badge>
      </header>
      <div class="recovery-body">
        <div class="reason-block">
          <strong>撤销原因</strong>
          <p>{{ detail.revocation_reason || '未记录撤销原因' }}</p>
        </div>

        <div class="unit-block">
          <strong>设备当前状态</strong>
          <ul>
            <li *ngFor="let unit of detail.units">
              <span class="unit-name">{{ unit.unit_code || ('设备 #' + unit.id) }}<small *ngIf="unit.name">{{ unit.name }}</small></span>
              <app-status-badge [value]="unit.state"></app-status-badge>
              <small class="unit-note" *ngIf="unit.notes">{{ unit.notes }}</small>
            </li>
          </ul>
        </div>

        <div class="progress-block">
          <strong>复查进度</strong>
          <div class="progress-counts">
            <span class="total">复查 {{ progress.total }} 项</span>
            <span class="pending">待处理 {{ progress.pending }}</span>
            <span class="passed">已通过 {{ progress.passed }}</span>
            <span class="failed" *ngIf="progress.failed > 0">未通过 {{ progress.failed }}</span>
          </div>
          <ul class="recheck-list" *ngIf="detail.rechecks.length">
            <li *ngFor="let item of detail.rechecks">
              <span><strong>{{ item.item_name }}</strong><small>{{ item.check_code }}<i *ngIf="item.ground_unit_id"> · 设备 #{{ item.ground_unit_id }}</i></small></span>
              <app-status-badge [value]="item.result" [label]="(kindText[item.kind] || '复查') + ' · ' + (statusText[item.result] || item.result)"></app-status-badge>
            </li>
          </ul>
          <p class="no-recheck" *ngIf="!detail.rechecks.length">检查员尚未新增故障复查，安全员暂不能重新决定。</p>
        </div>

        <div class="blocker-block" *ngIf="detail.recovery_blockers.length">
          <strong><mat-icon>warning</mat-icon>重新放行仍被拒绝</strong>
          <ul><li *ngFor="let blocker of detail.recovery_blockers">{{ blocker }}</li></ul>
        </div>
        <div class="ready-block" *ngIf="detail.recovery_complete">
          <mat-icon>check_circle</mat-icon>
          <span>设备已恢复、复查全部通过，安全员可以重新形成放行决定。</span>
        </div>

        <ng-container *ngIf="canInspect">
          <form [formGroup]="form" (ngSubmit)="addRecheck()" class="recheck-form">
            <strong>为故障设备新增复查</strong>
            <p class="form-hint">复查记录设备修复后的现场核查证据；设备未恢复、复查未通过或仍有待处理复查时，安全员无法重新决定。</p>
            <mat-form-field appearance="outline">
              <mat-label>故障设备</mat-label>
              <mat-select formControlName="ground_unit_id">
                <mat-option *ngFor="let unit of faultUnits" [value]="unit.id">{{ unit.unit_code }}（{{ statusText[unit.state] || unit.state }}）</mat-option>
              </mat-select>
            </mat-form-field>
            <mat-form-field appearance="outline"><mat-label>复查编码</mat-label><input matInput formControlName="check_code" placeholder="例如 RC-BLT-STOP-01"></mat-form-field>
            <mat-form-field appearance="outline" class="wide"><mat-label>复查项目</mat-label><input matInput formControlName="item_name" placeholder="例如 传送带急停开关复检"></mat-form-field>
            <mat-form-field appearance="outline"><mat-label>风险级别</mat-label><mat-select formControlName="risk_level"><mat-option value="low">{{ riskText.low }}</mat-option><mat-option value="medium">{{ riskText.medium }}</mat-option><mat-option value="high">{{ riskText.high }}</mat-option><mat-option value="critical">{{ riskText.critical }}</mat-option></mat-select></mat-form-field>
            <mat-form-field appearance="outline" class="wide"><mat-label>现场证据编号 / 文件名</mat-label><input matInput formControlName="evidence" placeholder="多个证据用逗号分隔，例如 blt-recheck-01.jpg"></mat-form-field>
            <button mat-flat-button type="submit" [disabled]="form.invalid || saving || !faultUnits.length"><mat-icon>post_add</mat-icon>新增复查</button>
            <p class="form-note" *ngIf="!faultUnits.length">关联设备均已退役，无法对退役设备新增复查。</p>
          </form>
        </ng-container>
      </div>
    </section>
  `,
  styles: [`
    .recovery-panel { border: 1px solid #f0d4cf; border-left: 3px solid #c2452f; background: #fdf8f7; border-radius: 5px; margin-bottom: 14px; }
    header { min-height: 44px; padding: 0 14px; display: flex; align-items: center; justify-content: space-between; border-bottom: 1px solid #f1e0dd; }
    header > span { display: flex; align-items: center; gap: 7px; font-weight: 600; color: #7a2d1d; }
    header mat-icon { color: #c2452f; font-size: 19px; width: 19px; height: 19px; }
    .recovery-body { padding: 12px 14px; display: flex; flex-direction: column; gap: 12px; }
    strong { display: block; color: #263a41; font-size: 12px; margin-bottom: 4px; }
    p, li, small { font-size: 12px; }
    .reason-block p { margin: 0; color: #8a3a28; line-height: 1.5; background: #fff; border: 1px solid #f1e0dd; border-radius: 4px; padding: 7px 9px; }
    .unit-block ul, .blocker-block ul, .recheck-list { list-style: none; margin: 0; padding: 0; }
    .unit-block li { display: flex; align-items: center; gap: 8px; padding: 5px 0; border-bottom: 1px dashed #ece2e0; flex-wrap: wrap; }
    .unit-name { display: flex; flex-direction: column; font-weight: 600; color: #37474f; min-width: 120px; }
    .unit-name small, .recheck-list small { color: #7c8b91; font-weight: 400; }
    .unit-note { color: #946b5f; flex-basis: 100%; }
    .progress-counts { display: flex; flex-wrap: wrap; gap: 6px; margin-bottom: 8px; }
    .progress-counts span { font-size: 11px; padding: 3px 8px; border-radius: 10px; background: #eef2f3; color: #52636a; }
    .progress-counts .pending { background: #fff4d6; color: #8a5b00; }
    .progress-counts .passed { background: #e7f6ed; color: #18794e; }
    .progress-counts .failed { background: #fdecea; color: #b42318; }
    .recheck-list li { display: flex; align-items: center; justify-content: space-between; gap: 8px; padding: 5px 0; border-bottom: 1px dashed #ece2e0; }
    .recheck-list strong { display: block; margin-bottom: 1px; color: #37474f; }
    .recheck-list i { font-style: normal; color: #93a1a7; }
    .no-recheck, .form-note, .form-hint { color: #8b989d; margin: 0; }
    .blocker-block { background: #fdecea; border: 1px solid #f5cfc9; border-radius: 4px; padding: 9px 11px; }
    .blocker-block strong { color: #b42318; display: flex; align-items: center; gap: 5px; }
    .blocker-block strong mat-icon { font-size: 16px; width: 16px; height: 16px; }
    .blocker-block ul li { color: #9b2c20; line-height: 1.6; }
    .blocker-block ul li::before { content: '• '; }
    .ready-block { display: flex; align-items: center; gap: 8px; background: #e7f6ed; border: 1px solid #c6e8d4; border-radius: 4px; padding: 9px 11px; color: #18794e; font-size: 12px; }
    .ready-block mat-icon { font-size: 18px; width: 18px; height: 18px; }
    .recheck-form { border-top: 1px solid #f1e0dd; padding-top: 10px; display: flex; flex-wrap: wrap; gap: 10px; align-items: flex-start; }
    .recheck-form strong { flex-basis: 100%; margin-bottom: 0; }
    .form-hint { flex-basis: 100%; margin-top: -6px; color: #946b5f; }
    .recheck-form mat-form-field { width: calc(50% - 5px); }
    .recheck-form .wide { width: 100%; }
    .recheck-form button { width: 100%; }
  `],
})
export class RecoveryPanelComponent {
  @Input({ required: true }) detail: RecoveryDetail | null = null;
  @Input() canInspect = false;
  @Output() changed = new EventEmitter<void>();

  private readonly fb = inject(FormBuilder);
  private readonly http = useHttp();
  private readonly snack = inject(MatSnackBar);
  readonly statusText = STATUS_TEXT;
  readonly kindText = CHECK_KIND_TEXT;
  readonly riskText = RISK_TEXT;
  saving = false;

  readonly form = this.fb.nonNullable.group({
    ground_unit_id: [null as number | null, Validators.required],
    check_code: ['', Validators.required],
    item_name: ['', Validators.required],
    risk_level: ['high' as 'low' | 'medium' | 'high' | 'critical', Validators.required],
    evidence: ['', Validators.required],
  });

  get progress() {
    return this.detail?.recheck_progress ?? { total: 0, pending: 0, passed: 0, failed: 0 };
  }

  get faultUnits(): Array<{ id: number; unit_code: string; state: string }> {
    return (this.detail?.units ?? [])
      .filter(unit => unit.state === 'inspection' || unit.state === 'blocked' || unit.state === 'available')
      .map(unit => ({ id: Number(unit.id), unit_code: unit.unit_code || `设备 #${unit.id}`, state: unit.state }));
  }

  addRecheck(): void {
    if (!this.detail || this.form.invalid) return;
    const value = this.form.getRawValue();
    const evidence = value.evidence.split(',').map(item => item.trim()).filter(Boolean);
    if (!evidence.length) {
      this.snack.open('请至少填写一个现场证据编号', '关闭', { duration: 3000 });
      return;
    }
    this.saving = true;
    checkCreateApi(this.http, {
      turnaround_id: this.detail.turnaround.id,
      ground_unit_id: value.ground_unit_id,
      check_code: value.check_code.trim().toUpperCase(),
      item_name: value.item_name.trim(),
      risk_level: value.risk_level,
      kind: 'recheck',
      evidence,
    }).subscribe({
      next: () => {
        this.saving = false;
        this.form.reset({ ground_unit_id: this.faultUnits[0]?.id ?? null, check_code: '', item_name: '', risk_level: 'high', evidence: '' });
        this.snack.open('故障复查已新增，可在检查清单中提交现场结论', '关闭', { duration: 2800 });
        this.changed.emit();
      },
      error: error => { this.saving = false; this.snack.open(parseHttpError(error), '关闭', { duration: 4500 }); },
    });
  }
}
