package station

import (
	"context"
	"strings"

	"example.com/paleo-prep-station/internal/contracts"
	"example.com/paleo-prep-station/internal/domain"
	"example.com/paleo-prep-station/internal/repository"
)

func (s *Service) LogProgress(
	ctx context.Context,
	planID string,
	request contracts.ProgressRequest,
) (domain.ProgressEntry, error) {
	if err := ctx.Err(); err != nil {
		return domain.ProgressEntry{}, err
	}

	var created domain.ProgressEntry
	err := s.repo.Mutate(func(m *repository.Mutation) error {
		entry, err := s.appendProgress(m, planID, request)
		if err != nil {
			return err
		}
		created = entry
		return nil
	})
	return created, err
}

// LogProgressBatch appends one progress entry per plan inside a single
// repository transaction, so the whole batch succeeds or rolls back together.
func (s *Service) LogProgressBatch(
	ctx context.Context,
	request contracts.ProgressBatchRequest,
) ([]domain.ProgressEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(request.Items) == 0 {
		return nil, contracts.Invalid("empty_progress_batch", "批量进度至少要包含一条记录")
	}
	seen := make(map[string]struct{}, len(request.Items))
	for _, item := range request.Items {
		planID := strings.TrimSpace(item.PlanID)
		if planID == "" {
			return nil, contracts.Invalid("required_field", "方案标识不能为空")
		}
		if _, ok := seen[planID]; ok {
			return nil, contracts.Conflict("duplicate_plan_in_batch", "批量提交中重复出现方案 "+planID)
		}
		seen[planID] = struct{}{}
	}

	var created []domain.ProgressEntry
	err := s.repo.Mutate(func(m *repository.Mutation) error {
		entries := make([]domain.ProgressEntry, 0, len(request.Items))
		for _, item := range request.Items {
			entry, err := s.appendProgress(m, strings.TrimSpace(item.PlanID), contracts.ProgressRequest{
				Stage:      item.Stage,
				Percent:    item.Percent,
				Note:       item.Note,
				RecordedBy: item.RecordedBy,
			})
			if err != nil {
				return err
			}
			entries = append(entries, entry)
		}
		created = entries
		return nil
	})
	if err != nil {
		return nil, err
	}
	return created, nil
}

// appendProgress applies a single entry inside an open mutation. Both the
// single-entry and batch flows share it so their state transitions match.
func (s *Service) appendProgress(
	m *repository.Mutation,
	planID string,
	request contracts.ProgressRequest,
) (domain.ProgressEntry, error) {
	plan, ok := m.GetPlan(planID)
	if !ok {
		return domain.ProgressEntry{}, notFoundPlan(planID)
	}
	specimen, ok := m.GetSpecimen(plan.SpecimenID)
	if !ok {
		return domain.ProgressEntry{}, notFoundSpecimen(plan.SpecimenID)
	}
	previous := m.ProgressForPlan(planID)
	entry, err := domain.NewProgressEntry(
		s.nextID("progress"),
		plan,
		previous,
		request.Stage,
		request.Percent,
		request.Note,
		request.RecordedBy,
		s.now(),
	)
	if err != nil {
		return domain.ProgressEntry{}, err
	}
	if plan.Status == domain.PlanApproved {
		if err := plan.Begin(s.now()); err != nil {
			return domain.ProgressEntry{}, err
		}
	}
	if request.Percent == 100 {
		if err := plan.Complete(s.now()); err != nil {
			return domain.ProgressEntry{}, err
		}
		if err := specimen.MarkStabilized(s.now()); err != nil {
			return domain.ProgressEntry{}, err
		}
	}
	m.AppendProgress(entry)
	m.PutPlan(plan)
	m.PutSpecimen(specimen)
	return entry, nil
}
