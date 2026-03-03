package ops

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/agentforge/agentforge/pkg/model"
	"github.com/agentforge/agentforge/pkg/state"
)

const (
	IssueMissingActiveRun      = "missing_active_run"
	IssueTaskRunStatusMismatch = "task_run_status_mismatch"
	IssueRunLastStepDrift      = "run_last_step_drift"
)

// DriftIssue captures one detected consistency drift.
type DriftIssue struct {
	Type     string `json:"type"`
	TenantID string `json:"tenant_id"`
	TaskID   string `json:"task_id"`
	RunID    string `json:"run_id,omitempty"`
	Detail   string `json:"detail"`
}

// CheckReport is a consistency scan result.
type CheckReport struct {
	ScannedTasks int          `json:"scanned_tasks"`
	ScannedRuns  int          `json:"scanned_runs"`
	Issues       []DriftIssue `json:"issues"`
}

// RepairReport summarizes applied repairs.
type RepairReport struct {
	Requested int      `json:"requested"`
	Applied   int      `json:"applied"`
	Failed    int      `json:"failed"`
	Errors    []string `json:"errors,omitempty"`
}

// ConsistencyChecker validates and repairs task/run/step state drift.
type ConsistencyChecker struct {
	store state.Store
}

// NewConsistencyChecker constructs a consistency checker.
func NewConsistencyChecker(store state.Store) *ConsistencyChecker {
	return &ConsistencyChecker{store: store}
}

// Check scans tenant state for drift.
func (c *ConsistencyChecker) Check(ctx context.Context, tenantID string, limit int) (*CheckReport, error) {
	if limit <= 0 {
		limit = 500
	}
	tasks, err := c.store.ListTasks(ctx, tenantID, nil, limit)
	if err != nil {
		return nil, fmt.Errorf("ops: list tasks: %w", err)
	}
	runs, err := c.store.ListRuns(ctx, tenantID, nil, limit*4)
	if err != nil {
		return nil, fmt.Errorf("ops: list runs: %w", err)
	}

	report := &CheckReport{
		ScannedTasks: len(tasks),
		ScannedRuns:  len(runs),
		Issues:       make([]DriftIssue, 0),
	}

	runByTaskRun := make(map[string]*model.Run, len(runs))
	for _, run := range runs {
		runByTaskRun[run.TaskID+"#"+run.RunID] = run
	}

	for _, taskObj := range tasks {
		if ctx.Err() != nil {
			return report, ctx.Err()
		}
		activeKey := taskObj.TaskID + "#" + taskObj.ActiveRunID
		activeRun, ok := runByTaskRun[activeKey]
		if !ok {
			report.Issues = append(report.Issues, DriftIssue{
				Type:     IssueMissingActiveRun,
				TenantID: taskObj.TenantID,
				TaskID:   taskObj.TaskID,
				RunID:    taskObj.ActiveRunID,
				Detail:   "task.active_run_id does not exist",
			})
			continue
		}

		if mismatch := taskRunStatusMismatch(taskObj.Status, activeRun.Status); mismatch != "" {
			report.Issues = append(report.Issues, DriftIssue{
				Type:     IssueTaskRunStatusMismatch,
				TenantID: taskObj.TenantID,
				TaskID:   taskObj.TaskID,
				RunID:    activeRun.RunID,
				Detail:   mismatch,
			})
		}
	}

	for _, run := range runs {
		if ctx.Err() != nil {
			return report, ctx.Err()
		}
		steps, err := c.store.ListSteps(ctx, run.RunID, 0, 100000)
		if err != nil {
			continue
		}
		maxStep := -1
		for _, step := range steps {
			if step.StepIndex > maxStep {
				maxStep = step.StepIndex
			}
		}
		if maxStep >= 0 && run.LastStepIndex != maxStep {
			report.Issues = append(report.Issues, DriftIssue{
				Type:     IssueRunLastStepDrift,
				TenantID: run.TenantID,
				TaskID:   run.TaskID,
				RunID:    run.RunID,
				Detail:   fmt.Sprintf("run.last_step_index=%d expected=%d", run.LastStepIndex, maxStep),
			})
		}
	}

	return report, nil
}

// Repair applies best-effort fixes for known drift issues.
func (c *ConsistencyChecker) Repair(ctx context.Context, issues []DriftIssue) (*RepairReport, error) {
	report := &RepairReport{Requested: len(issues)}
	for _, issue := range issues {
		if ctx.Err() != nil {
			return report, ctx.Err()
		}
		var err error
		switch issue.Type {
		case IssueTaskRunStatusMismatch:
			err = c.repairTaskRunStatus(ctx, issue)
		case IssueRunLastStepDrift:
			err = c.repairLastStep(ctx, issue)
		case IssueMissingActiveRun:
			err = c.repairMissingActiveRun(ctx, issue)
		default:
			err = fmt.Errorf("unsupported issue type: %s", issue.Type)
		}
		if err != nil {
			report.Failed++
			report.Errors = append(report.Errors, fmt.Sprintf("%s task=%s run=%s: %v", issue.Type, issue.TaskID, issue.RunID, err))
			continue
		}
		report.Applied++
	}
	return report, nil
}

func (c *ConsistencyChecker) repairTaskRunStatus(ctx context.Context, issue DriftIssue) error {
	run, err := c.store.GetRun(ctx, issue.TaskID, issue.RunID)
	if err != nil {
		return err
	}
	target := mapRunToTaskStatus(run.Status)
	return c.store.UpdateTaskStatusForRun(ctx, issue.TaskID, issue.RunID, []model.TaskStatus{
		model.TaskStatusQueued, model.TaskStatusRunning, model.TaskStatusSucceeded, model.TaskStatusFailed, model.TaskStatusAborted,
	}, target)
}

func (c *ConsistencyChecker) repairLastStep(ctx context.Context, issue DriftIssue) error {
	want, err := parseExpectedFromDetail(issue.Detail)
	if err != nil {
		return err
	}
	return c.store.UpdateLastStepIndex(ctx, issue.TaskID, issue.RunID, want)
}

func (c *ConsistencyChecker) repairMissingActiveRun(ctx context.Context, issue DriftIssue) error {
	// Conservative repair: mark task as FAILED if its active run is missing.
	return c.store.UpdateTaskStatus(ctx, issue.TaskID, []model.TaskStatus{
		model.TaskStatusQueued, model.TaskStatusRunning, model.TaskStatusSucceeded, model.TaskStatusFailed, model.TaskStatusAborted,
	}, model.TaskStatusFailed)
}

func parseExpectedFromDetail(detail string) (int, error) {
	parts := strings.Split(detail, "expected=")
	if len(parts) != 2 {
		return 0, fmt.Errorf("invalid detail: %q", detail)
	}
	n, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil {
		return 0, err
	}
	return n, nil
}

func taskRunStatusMismatch(taskStatus model.TaskStatus, runStatus model.RunStatus) string {
	switch runStatus {
	case model.RunStatusQueued:
		if taskStatus != model.TaskStatusQueued {
			return fmt.Sprintf("task=%s run=%s", taskStatus, runStatus)
		}
	case model.RunStatusRunning:
		if taskStatus != model.TaskStatusRunning {
			return fmt.Sprintf("task=%s run=%s", taskStatus, runStatus)
		}
	case model.RunStatusSucceeded:
		if taskStatus != model.TaskStatusSucceeded {
			return fmt.Sprintf("task=%s run=%s", taskStatus, runStatus)
		}
	case model.RunStatusFailed:
		if taskStatus != model.TaskStatusFailed {
			return fmt.Sprintf("task=%s run=%s", taskStatus, runStatus)
		}
	case model.RunStatusAborted:
		if taskStatus != model.TaskStatusAborted {
			return fmt.Sprintf("task=%s run=%s", taskStatus, runStatus)
		}
	}
	return ""
}

func mapRunToTaskStatus(runStatus model.RunStatus) model.TaskStatus {
	switch runStatus {
	case model.RunStatusQueued:
		return model.TaskStatusQueued
	case model.RunStatusRunning:
		return model.TaskStatusRunning
	case model.RunStatusSucceeded:
		return model.TaskStatusSucceeded
	case model.RunStatusFailed:
		return model.TaskStatusFailed
	case model.RunStatusAborted:
		return model.TaskStatusAborted
	default:
		return model.TaskStatusFailed
	}
}
