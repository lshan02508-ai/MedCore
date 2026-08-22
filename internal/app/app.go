package app

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"time"

	backendopenai "github.com/lshan02508-ai/GOLLM/internal/adapters/backend/openai"
	"github.com/lshan02508-ai/GOLLM/internal/adapters/config"
	ingressopenai "github.com/lshan02508-ai/GOLLM/internal/adapters/ingress/openai"
	"github.com/lshan02508-ai/GOLLM/internal/core/cost"
	coremodel "github.com/lshan02508-ai/GOLLM/internal/core/model"
	corescheduler "github.com/lshan02508-ai/GOLLM/internal/core/scheduler"
	"github.com/lshan02508-ai/GOLLM/internal/proxy"
	"github.com/lshan02508-ai/GOLLM/internal/registry"
	"github.com/lshan02508-ai/GOLLM/internal/telemetry"
)

type Application struct {
	Handler   http.Handler
	monitor   *registry.HealthMonitor
	scheduler *corescheduler.Scheduler
}

func (a *Application) Start(ctx context.Context) {
	a.scheduler.Start(ctx)
	a.monitor.Start(ctx)
}

// Shutdown stops admission and waits until every dispatched request has
// returned its scheduling lease or the caller's deadline expires.
func (a *Application) Shutdown(ctx context.Context) error {
	return a.scheduler.Shutdown(ctx)
}

func New(cfg config.Runtime, logger *slog.Logger) (*Application, error) {
	if logger == nil {
		logger = slog.Default()
	}
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: cfg.ResponseHeaderTimeout}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          cfg.Backend.MaxIdleConns,
		MaxIdleConnsPerHost:   cfg.Backend.MaxIdleConnsPerHost,
		IdleConnTimeout:       cfg.IdleConnTimeout,
		ResponseHeaderTimeout: cfg.ResponseHeaderTimeout,
	}
	metrics := &telemetry.Metrics{}
	client := &http.Client{Transport: transport}
	workerDefinitions := make([]registry.Worker, 0, len(cfg.Workers))
	for _, worker := range cfg.Workers {
		workerDefinitions = append(workerDefinitions, registry.Worker{ID: worker.ID, Endpoint: worker.Endpoint, Models: worker.Models, MaxConcurrency: worker.MaxConcurrency, MaxInflightCost: worker.MaxInflightCost, InitialServiceRate: worker.InitialServiceRate})
	}
	workerRegistry, err := registry.NewMemory(workerDefinitions)
	if err != nil {
		return nil, err
	}
	workerHandlers := make(map[string]http.Handler, len(cfg.Workers))
	healthTargets := make([]registry.HealthTarget, 0, len(cfg.Workers))
	models := make([]string, 0, len(cfg.Workers))
	for _, worker := range cfg.Workers {
		metricModel := "*"
		if len(worker.Models) == 1 {
			metricModel = worker.Models[0]
		}
		backendProxy, proxyErr := proxy.New(proxy.Options{BackendURL: worker.Endpoint, APIKey: worker.APIKey, Client: client, MaxBodyBytes: cfg.Server.MaxRequestBodyBytes, RequestTimeout: cfg.RequestTimeout, FirstTokenTimeout: cfg.FirstTokenTimeout, StreamIdleTimeout: cfg.StreamIdleTimeout, WorkerID: worker.ID, MetricModel: metricModel, Logger: logger, Metrics: metrics})
		if proxyErr != nil {
			return nil, proxyErr
		}
		checker, checkerErr := backendopenai.NewHealthChecker(worker.Endpoint, worker.APIKey, client)
		if checkerErr != nil {
			return nil, checkerErr
		}
		workerHandlers[worker.ID] = backendProxy
		healthTargets = append(healthTargets, registry.HealthTarget{WorkerID: worker.ID, Probe: checker})
		for _, model := range worker.Models {
			if model != "*" {
				models = append(models, model)
			}
		}
	}
	slices.Sort(models)
	models = slices.Compact(models)
	allowClientClassHint := false
	if cfg.WorkloadClassification.AllowClientHint != nil {
		allowClientClassHint = *cfg.WorkloadClassification.AllowClientHint
	}
	allowClientTaskHint := false
	if cfg.TokenBudget.AllowClientTaskHint != nil {
		allowClientTaskHint = *cfg.TokenBudget.AllowClientTaskHint
	}
	normalizer, err := ingressopenai.NewNormalizer(ingressopenai.NormalizerOptions{
		AllowClientClassHint:     allowClientClassHint,
		AllowClientTaskHint:      allowClientTaskHint,
		RealtimeMaxInputBytes:    cfg.WorkloadClassification.RealtimeMaxInputBytes,
		RealtimeMaxOutputTokens:  cfg.WorkloadClassification.RealtimeMaxOutputTokens,
		LongContextMinInputBytes: cfg.WorkloadClassification.LongContextMinInputBytes,
		BatchMinOutputTokens:     cfg.WorkloadClassification.BatchMinOutputTokens,
	})
	if err != nil {
		return nil, err
	}
	taskOutputTokens := make(map[coremodel.TaskKind]int64, len(cfg.TokenBudget.TaskOutputTokens))
	for kind, tokens := range cfg.TokenBudget.TaskOutputTokens {
		taskOutputTokens[coremodel.TaskKind(kind)] = tokens
	}
	workloadOutputTokens := make(map[coremodel.WorkloadClass]int64, len(cfg.TokenBudget.WorkloadOutputTokens))
	for class, tokens := range cfg.TokenBudget.WorkloadOutputTokens {
		workloadOutputTokens[coremodel.WorkloadClass(class)] = tokens
	}
	estimator, err := cost.NewEstimator(cost.EstimatorOptions{
		BytesPerToken: cfg.TokenBudget.BytesPerToken, MessageOverheadTokens: cfg.TokenBudget.MessageOverheadTokens,
		DefaultContextWindowTokens: cfg.TokenBudget.DefaultContextWindowTokens, DefaultOutputTokens: cfg.TokenBudget.DefaultOutputTokens,
		TaskOutputTokens: taskOutputTokens, WorkloadOutputTokens: workloadOutputTokens,
		ModelContextWindowTokens: cfg.TokenBudget.ModelContextWindowTokens,
	})
	if err != nil {
		return nil, err
	}
	schedulerPools := make([]corescheduler.PoolConfig, 0, len(cfg.RuntimeModelPools))
	for _, modelPool := range cfg.RuntimeModelPools {
		schedulerPools = append(schedulerPools, corescheduler.PoolConfig{Model: modelPool.Model, MaxInflightCost: modelPool.MaxInflightCost})
	}
	schedulerWorkers := make([]corescheduler.WorkerConfig, 0, len(cfg.Workers))
	for _, worker := range cfg.Workers {
		schedulerWorkers = append(schedulerWorkers, corescheduler.WorkerConfig{ID: worker.ID, MaxInflightCost: worker.MaxInflightCost, InitialServiceRate: worker.InitialServiceRate})
	}
	requestScheduler, err := corescheduler.New(corescheduler.Options{
		Registry: workerRegistry, Pools: schedulerPools, Workers: schedulerWorkers,
		QueueCapacity: cfg.Scheduler.QueueCapacity, MaxQueuedCost: cfg.Scheduler.MaxQueuedCost,
		MaxQueueWait: cfg.SchedulerMaxQueueWait, RetryAfter: cfg.SchedulerRetryAfter,
		AgingInterval: cfg.SchedulerAgingInterval, MaxBypass: cfg.Scheduler.MaxBypass,
		EWMAAlpha: cfg.Scheduler.EWMAAlpha, MinServiceRate: cfg.Scheduler.MinServiceRate,
		MaxServiceRate: cfg.Scheduler.MaxServiceRate, Metrics: metrics,
	})
	if err != nil {
		return nil, err
	}
	pool, err := proxy.NewPool(proxy.PoolOptions{
		Availability: workerRegistry, Normalizer: normalizer, Estimator: estimator, Scheduler: requestScheduler,
		Workers: workerHandlers, Models: models, MaxBodyBytes: cfg.Server.MaxRequestBodyBytes,
		PrefillWeight: cfg.Scheduler.PrefillWeight, DecodeWeight: cfg.Scheduler.DecodeWeight, ClassSLO: cfg.SchedulerClassSLO,
		Logger: logger,
	})
	if err != nil {
		return nil, err
	}
	checker := &poolReadiness{registry: workerRegistry}
	monitor, err := registry.NewHealthMonitor(registry.HealthMonitorOptions{
		Registry: workerRegistry, Targets: healthTargets, Interval: cfg.WorkerHealthInterval, Timeout: cfg.WorkerHealthTimeout,
		UnhealthyThreshold: cfg.WorkerHealth.UnhealthyThreshold, HealthyThreshold: cfg.WorkerHealth.HealthyThreshold, Metrics: metrics,
	})
	if err != nil {
		return nil, err
	}
	metrics.SetWorkerCounts(int64(len(cfg.Workers)), 0)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.Handle("GET /readyz", readinessHandler(checker, cfg.ReadinessTimeout, logger))
	mux.Handle("GET /metrics", metrics)
	mux.Handle("POST /v1/chat/completions", pool)
	mux.Handle("GET /v1/models", pool)
	return &Application{Handler: mux, monitor: monitor, scheduler: requestScheduler}, nil
}

// NewHandler retains the earlier construction API for tests and embedders. It
// performs one bounded health pass; long-running processes should use New and
// Start so checks continue in the background.
func NewHandler(cfg config.Runtime, logger *slog.Logger) (http.Handler, error) {
	application, err := New(cfg, logger)
	if err != nil {
		return nil, err
	}
	application.monitor.CheckOnce(context.Background())
	application.scheduler.Start(context.Background())
	return application.Handler, nil
}

type readinessChecker interface {
	Ready(context.Context) error
}

func readinessHandler(checker readinessChecker, timeout time.Duration, logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()
		if err := checker.Ready(ctx); err != nil {
			logger.Debug("gateway is not ready", "error", err)
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ready\n"))
	})
}
