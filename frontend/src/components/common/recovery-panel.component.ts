import { CommonModule } from '@angular/common';
import { Component, Input } from '@angular/core';
import { MatIconModule } from '@angular/material/icon';
import { MatProgressBarModule } from '@angular/material/progress-bar';
import { UNIT_STATE_TEXT } from '../../constants/enums';
import { ClearanceDecision, TurnaroundReadiness, UnitState } from '../../types';
import { EvidenceListComponent } from './evidence-list.component';
import { StatusBadgeComponent } from './status-badge.component';

@Component({
  selector: 'app-recovery-panel',
  standalone: true,
  imports: [CommonModule, MatIconModule, MatProgressBarModule, StatusBadgeComponent, EvidenceListComponent],
  template: `
    <section class="recovery" *ngIf="readiness && readiness.clearance_state === 'revoked'">
      <header>
        <span><mat-icon>build_circle</mat-icon>撤销恢复流程</span>
        <app-status-badge value="revoked"></app-status-badge>
      </header>
      <div class="body">
        <p class="reason"><strong>撤销原因</strong>{{ decision?.reason || readiness.clearance_reason || '记录未填写撤销原因' }}</p>
        <div class="evidence-line" *ngIf="decision?.evidence?.length"><strong>撤销证据</strong><app-evidence-list [items]="decision?.evidence || []"></app-evidence-list></div>
        <div class="units">
          <h3>设备当前状态</h3>
          <div class="unit-list">
            <span *ngFor="let unit of readiness.units">
              <strong>{{ unit.unit_code }}</strong>
              <app-status-badge [value]="unit.state"></app-status-badge>
            </span>
          </div>
        </div>
        <div class="recheck">
          <h3>复查进度</h3>
          <div class="recheck-metrics">
            <span><strong>{{ readiness.recheck_total }}</strong><small>复查项</small></span>
            <span><strong class="warn">{{ readiness.recheck_pending }}</strong><small>待处理</small></span>
            <span><strong class="good">{{ readiness.recheck_passed }}</strong><small>已通过</small></span>
            <span><strong class="danger">{{ readiness.recheck_failed }}</strong><small>未通过</small></span>
          </div>
          <mat-progress-bar mode="determinate" [value]="recheckPercent"></mat-progress-bar>
        </div>
        <ul class="blockers" *ngIf="readiness.blockers.length; else allClear">
          <li *ngFor="let blocker of readiness.blockers"><mat-icon>error_outline</mat-icon>{{ blockerText(blocker) }}</li>
        </ul>
        <ng-template #allClear><p class="all-clear"><mat-icon>check_circle</mat-icon>阻断项已清零，安全放行员可重新决定。</p></ng-template>
      </div>
    </section>
  `,
  styles: [`
    .recovery { border: 1px solid #e8d3d0; border-left: 3px solid #b42318; background: #fff8f7; border-radius: 5px; }
    header { min-height: 44px; padding: 0 14px; display: flex; align-items: center; justify-content: space-between; border-bottom: 1px solid #efdedc; }
    header > span { display: flex; align-items: center; gap: 7px; font-weight: 600; color: #7a2820; }
    header mat-icon { color: #b42318; font-size: 19px; width: 19px; height: 19px; }
    .body { padding: 12px 14px; }
    p { margin: 0 0 10px; color: #576a71; font-size: 12px; line-height: 1.5; }
    p strong, .evidence-line strong { display: block; color: #7a2820; margin-bottom: 3px; }
    .evidence-line { margin-bottom: 11px; }
    h3 { margin: 0 0 8px; color: #273b42; font-size: 12px; }
    .units, .recheck { padding: 9px 0; border-top: 1px dashed #e7d9d7; }
    .unit-list { display: flex; flex-wrap: wrap; gap: 8px; }
    .unit-list span { display: inline-flex; align-items: center; gap: 6px; padding: 5px 9px; border: 1px solid #e2d6d4; border-radius: 4px; background: #fff; }
    .unit-list strong { color: #354950; font-size: 11px; }
    .recheck-metrics { display: grid; grid-template-columns: repeat(4, 1fr); gap: 6px; margin-bottom: 9px; text-align: center; }
    .recheck-metrics span { padding: 6px 4px; background: #fff; border: 1px solid #eee1df; border-radius: 4px; }
    .recheck-metrics strong, .recheck-metrics small { display: block; }
    .recheck-metrics strong { color: #34484f; font-size: 17px; line-height: 1.1; }
    .recheck-metrics strong.good { color: #16835a; }
    .recheck-metrics strong.warn { color: #b96708; }
    .recheck-metrics strong.danger { color: #b42318; }
    .recheck-metrics small { margin-top: 2px; color: #8a969c; font-size: 10px; }
    .blockers { list-style: none; margin: 10px 0 0; padding: 9px 11px; border-radius: 4px; background: #fff0ed; }
    .blockers li { display: flex; align-items: center; gap: 6px; margin-bottom: 5px; color: #a63b32; font-size: 11px; line-height: 1.4; }
    .blockers li:last-child { margin-bottom: 0; }
    .blockers mat-icon { font-size: 15px; width: 15px; height: 15px; flex: none; }
    .all-clear { display: flex; align-items: center; gap: 7px; margin: 10px 0 0; padding: 9px 11px; border-radius: 4px; background: #e9f7ef; color: #1d7f57; font-size: 11px; }
    .all-clear mat-icon { font-size: 17px; width: 17px; height: 17px; }
  `],
})
export class RecoveryPanelComponent {
  @Input() readiness: TurnaroundReadiness | null = null;
  @Input() decision: ClearanceDecision | null = null;
  readonly unitText = UNIT_STATE_TEXT;

  get recheckPercent(): number {
    if (!this.readiness || this.readiness.recheck_total === 0) return 0;
    const done = this.readiness.recheck_passed + this.readiness.recheck_failed;
    return Math.round((done / this.readiness.recheck_total) * 100);
  }

  blockerText(blocker: string): string {
    if (blocker === 'no recheck filed after revocation') {
      return '撤销后尚未新增复查项，请检查员先为故障设备建立复查';
    }
    if (blocker.startsWith('pending check: ')) {
      return `存在待处理检查：${blocker.slice('pending check: '.length)}`;
    }
    if (blocker.startsWith('failed check: ')) {
      return `存在未通过检查：${blocker.slice('failed check: '.length)}`;
    }
    if (blocker.startsWith('ground unit ') && blocker.includes(' is ')) {
      const body = blocker.slice('ground unit '.length);
      const [code, ...rest] = body.split(' is ');
      const state = rest.join(' is ');
      return `设备 ${code} 当前状态为“${this.unitText[state as UnitState] || state}”，尚未恢复可用`;
    }
    return blocker;
  }
}
