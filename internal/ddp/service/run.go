// Package service composes the API and scheduler runtime roles.
package service

import (
	"context"
	"errors"
	"io"
	"math"
	"path/filepath"

	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/config"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/metrics"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/scheduler"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/serving"
	"github.com/Profreshor/Dealership-Data-Platform/internal/ddp/web"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Options struct{ APIURL, SchedulerURL, APIMetricsAddr, SchedulerMetricsAddr string }

func RunAll(ctx context.Context, cfg *config.Config, root string, options Options, logs io.Writer, register func(*web.Registry)) error {
	if cfg == nil {
		return errors.New("service config is required")
	}
	if options.APIURL == "" || options.SchedulerURL == "" {
		return errors.New("service database URLs are required")
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	errs := make(chan error, 2)
	go func() { errs <- serving.Run(runCtx, cfg, options.APIURL, options.APIMetricsAddr, register) }()
	go func() {
		errs <- RunScheduler(runCtx, cfg, options.SchedulerURL, root, options.SchedulerMetricsAddr, logs)
	}()
	first := <-errs
	if first == nil && ctx.Err() == nil {
		first = errors.New("service stopped unexpectedly")
	}
	cancel()
	second := <-errs
	return errors.Join(first, second)
}

func RunScheduler(ctx context.Context, cfg *config.Config, url, root, metricsAddr string, logs io.Writer) error {
	if cfg == nil || url == "" {
		return errors.New("scheduler requires config and database URL")
	}
	if cfg.Scheduler.MaxWorkers < 1 || int64(cfg.Scheduler.MaxWorkers) >= math.MaxInt32 {
		return errors.New("scheduler max_workers exceeds database connection limit")
	}
	pc, err := pgxpool.ParseConfig(url)
	if err != nil {
		return errors.New("invalid scheduler database URL")
	}
	pc.MaxConns = max(pc.MaxConns, int32(cfg.Scheduler.MaxWorkers+1))
	pool, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		return errors.New("open scheduler pool")
	}
	defer pool.Close()
	monitor := metrics.New(pool, cfg, nil)
	return metrics.Run(ctx, metricsAddr, monitor.Handler(), func(workCtx context.Context) error {
		return scheduler.Run(workCtx, pool, cfg, filepath.Clean(root), logs)
	})
}
