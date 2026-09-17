package server

import (
	"context"
	"time"
)

func (s *Server) runApprovalReaper(ctx context.Context) {
	s.runReaper(ctx, "approval reaper", s.approvalPollInterval, func(runContext context.Context) error {
		commands, err := s.store.ExpireApprovals(runContext, s.reaperBatchSize)
		if err != nil {
			return err
		}
		s.metrics.approvalTimeouts.Add(uint64(len(commands)))
		for index := range commands {
			s.dispatch(&commands[index])
		}
		return nil
	})
}

func (s *Server) runHibernationReaper(ctx context.Context) {
	s.runReaper(ctx, "hibernation reaper", s.hibernationPollInterval, func(runContext context.Context) error {
		commands, err := s.store.HibernateIdleBoxes(runContext, s.reaperBatchSize)
		if err != nil {
			return err
		}
		for index := range commands {
			s.dispatch(&commands[index])
		}
		return nil
	})
}

func (s *Server) runAutomationScheduler(ctx context.Context) {
	s.runReaper(ctx, "automation scheduler", s.schedulePollInterval, func(runContext context.Context) error {
		commands, err := s.store.ProcessDueSchedules(runContext, s.reaperBatchSize)
		if err != nil {
			return err
		}
		for index := range commands {
			s.dispatch(&commands[index])
		}
		commands, err = s.store.DispatchAutomationRuns(runContext, s.reaperBatchSize)
		if err != nil {
			return err
		}
		s.observeScheduleDispatches(commands)
		for index := range commands {
			s.dispatch(&commands[index])
		}
		return nil
	})
}

func (s *Server) runRetentionReaper(ctx context.Context) {
	s.runReaper(ctx, "retention reaper", s.retentionPollInterval, func(runContext context.Context) error {
		now := time.Now().UTC()
		_, err := s.store.PruneOperationalData(
			runContext,
			now.Add(-s.operationalRetention),
			now.Add(-s.auditRetention),
			s.reaperBatchSize,
		)
		return err
	})
}

func (s *Server) runReaper(ctx context.Context, name string, interval time.Duration, operation func(context.Context) error) {
	backoff := time.Second
	for {
		if err := ctx.Err(); err != nil {
			return
		}
		err := operation(ctx)
		delay := interval
		if err != nil {
			s.metrics.reaperErrors.Add(1)
			s.logError(name, err)
			s.metrics.recordFailure(name, name+" failed")
			delay = backoff
			backoff *= 2
			if backoff > 30*time.Second {
				backoff = 30 * time.Second
			}
		} else {
			backoff = time.Second
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return
		case <-timer.C:
		}
	}
}
