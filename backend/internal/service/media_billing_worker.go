package service

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

type mediaReconciliationRepository interface {
	ClaimMediaReconciliation(context.Context, time.Time, time.Time, int) ([]*MediaGenerationJob, error)
	RescheduleMediaReconciliation(context.Context, string, *time.Time) error
}

func (s *MediaGenerationService) Start() {
	if s == nil {
		return
	}
	s.workerMu.Lock()
	defer s.workerMu.Unlock()
	if s.workerCancel != nil {
		return
	}
	if _, ok := s.jobRepo.(mediaReconciliationRepository); !ok {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.workerCancel = cancel
	s.workerDone = make(chan struct{})
	go func() {
		defer close(s.workerDone)
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			if err := s.ReconcileVideoJobs(ctx); err != nil && ctx.Err() == nil {
				slog.Error("media reconciliation failed", "error", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}
func (s *MediaGenerationService) Stop() {
	if s == nil {
		return
	}
	s.workerMu.Lock()
	cancel, done := s.workerCancel, s.workerDone
	s.workerMu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	<-done
}
func (s *MediaGenerationService) ReconcileVideoJobs(ctx context.Context) error {
	repo, ok := s.jobRepo.(mediaReconciliationRepository)
	if !ok {
		return nil
	}
	now := time.Now().UTC()
	jobs, err := repo.ClaimMediaReconciliation(ctx, now, now.Add(2*time.Minute), 10)
	if err != nil {
		return err
	}
	var wg sync.WaitGroup
	sem := make(chan struct{}, 5)
	for _, job := range jobs {
		if ctx.Err() != nil {
			break
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(job *MediaGenerationJob) {
			defer wg.Done()
			defer func() { <-sem }()
			taskCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			var account *Account
			var err error
			if !isCompletedMediaJob(job.Status) || MediaJobNeedsUsageRefresh(job) {
				account, err = s.GetAccountByID(taskCtx, job.AccountID)
			}
			var updated *MediaGenerationJob
			if err == nil {
				if job.Kind == MediaJobKindVideoGeneration {
					updated, err = s.RefreshVideoJob(taskCtx, job, account)
				} else {
					err = s.recordMediaUsage(taskCtx, job)
					updated = job
				}
			}
			next := time.Now().UTC().Add(15 * time.Second)
			var at *time.Time = &next
			if err != nil {
				next = time.Now().UTC().Add(time.Minute)
				slog.Error("media task reconciliation failed", "job_id", job.PublicID, "error", err)
			} else if updated != nil && isCompletedMediaJob(updated.Status) {
				at = nil
			}
			if err := repo.RescheduleMediaReconciliation(taskCtx, job.PublicID, at); err != nil && ctx.Err() == nil {
				slog.Warn("media task reschedule failed", "job_id", job.PublicID, "error", err)
			}
		}(job)
	}
	wg.Wait()
	return ctx.Err()
}
