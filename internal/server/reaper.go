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
