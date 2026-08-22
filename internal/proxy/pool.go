package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/lshan02508-ai/GOLLM/internal/core/cost"
	coremodel "github.com/lshan02508-ai/GOLLM/internal/core/model"
	"github.com/lshan02508-ai/GOLLM/internal/core/scheduler"
	"github.com/lshan02508-ai/GOLLM/internal/registry"
	"github.com/lshan02508-ai/GOLLM/pkg/headers"
)

type workerAvailability interface {
	Available(string) []registry.WorkerSnapshot
}
type requestNormalizer interface {
	Normalize([]byte, http.Header) (coremodel.RequestMetadata, error)
}
type tokenEstimator interface {
	Estimate(coremodel.RequestMetadata) (cost.TokenBudget, error)
}
type requestScheduler interface {
	Schedule(context.Context, scheduler.Request) (*scheduler.Grant, error)
}

type PoolOptions struct {
	Availability  workerAvailability
	Normalizer    requestNormalizer
	Estimator     tokenEstimator
	Scheduler     requestScheduler
	Workers       map[string]http.Handler
	Models        []string
	MaxBodyBytes  int64
	PrefillWeight int64
	DecodeWeight  int64
	ClassSLO      map[string]time.Duration
	Logger        *slog.Logger
}

type Pool struct {
	availability  workerAvailability
	normalizer    requestNormalizer
	estimator     tokenEstimator
	scheduler     requestScheduler
	workers       map[string]http.Handler
	models        []string
	maxBodyBytes  int64
	prefillWeight int64
	decodeWeight  int64
	classSLO      map[string]time.Duration
	logger        *slog.Logger
}

func NewPool(options PoolOptions) (*Pool, error) {
	if options.Availability == nil || options.Normalizer == nil || options.Estimator == nil || options.Scheduler == nil {
		return nil, errors.New("availability, normalizer, token estimator, and scheduler are required")
	}
	if len(options.Workers) == 0 {
		return nil, errors.New("at least one worker handler is required")
	}
	if options.MaxBodyBytes <= 0 {
		options.MaxBodyBytes = 32 << 20
	}
	if options.PrefillWeight <= 0 || options.DecodeWeight <= 0 {
		return nil, errors.New("request cost weights must be positive")
	}
	if options.Logger == nil {
		options.Logger = slog.Default()
	}
	models := slices.Clone(options.Models)
	slices.Sort(models)
	models = slices.Compact(models)
	classSLO := make(map[string]time.Duration, len(options.ClassSLO))
	for class, slo := range options.ClassSLO {
		classSLO[class] = slo
	}
	return &Pool{availability: options.Availability, normalizer: options.Normalizer, estimator: options.Estimator, scheduler: options.Scheduler, workers: options.Workers, models: models, maxBodyBytes: options.MaxBodyBytes, prefillWeight: options.PrefillWeight, decodeWeight: options.DecodeWeight, classSLO: classSLO, logger: options.Logger}, nil
}

func (p *Pool) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && r.URL.Path == "/v1/models" {
		if len(p.models) > 0 {
			p.serveModels(w)
			return
		}
		workers := p.availability.Available("")
		if len(workers) == 0 {
			writeOpenAIError(w, http.StatusServiceUnavailable, "no_available_worker", "no healthy inference worker is available")
			return
		}
		handler, ok := p.workers[workers[0].ID]
		if !ok {
			writeOpenAIError(w, http.StatusInternalServerError, "gateway_error", "selected worker has no backend handler")
			return
		}
		w.Header().Set("X-GoLLM-Worker-ID", workers[0].ID)
		handler.ServeHTTP(w, r)
		return
	}
	if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
		writeOpenAIError(w, http.StatusNotFound, "not_found", "the requested endpoint is not available")
		return
	}
	body, err := p.readRequestBody(w, r)
	if err != nil {
		return
	}
	metadata, err := p.normalizer.Normalize(body, r.Header)
	if err != nil {
		p.writeProcessingError(w, err, "failed to normalize request")
		return
	}
	p.writeMetadataHeaders(w.Header(), metadata)
	budget, err := p.estimator.Estimate(metadata)
	if err != nil {
		p.writeProcessingError(w, err, "failed to estimate token budget")
		return
	}
	requestCost, err := scheduler.CalculateRequestCost(budget, p.prefillWeight, p.decodeWeight)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_cost", err.Error())
		return
	}
	w.Header().Set(headers.PromptTokens, strconv.FormatInt(budget.PromptTokensEstimated, 10))
	w.Header().Set(headers.ReservedTokens, strconv.FormatInt(budget.TotalReservedTokens, 10))
	w.Header().Set(headers.BudgetSource, string(budget.Source))
	w.Header().Set(headers.RequestCost, strconv.FormatInt(requestCost, 10))
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	r.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
	ctx := coremodel.WithRequestMetadata(r.Context(), metadata)
	r = r.WithContext(cost.WithTokenBudget(ctx, budget))
	scheduleRequest := scheduler.Request{ID: metadata.ID, Model: metadata.Model, Class: string(metadata.WorkloadClass), Cost: requestCost, Deadline: p.deadline(r.Context(), metadata)}
	grant, err := p.scheduler.Schedule(r.Context(), scheduleRequest)
	if err != nil {
		p.logRejected(metadata, scheduleRequest, err)
		p.writeSchedulerError(w, err)
		return
	}
	p.logger.Info("request dispatched",
		"request_id", metadata.ID, "model", metadata.Model,
		"workload_class", metadata.WorkloadClass, "task_kind", metadata.TaskKind,
		"request_cost", requestCost, "queue_wait_ms", grant.QueueWait.Milliseconds(),
		"deadline_remaining_ms", deadlineRemainingMS(scheduleRequest.Deadline),
		"worker_id", grant.Worker.ID, "worker_inflight_cost", grant.WorkerInflightCost,
		"worker_service_rate", grant.ServiceRate, "scheduler_reason", grant.Reason,
	)
	handler, exists := p.workers[grant.Worker.ID]
	if !exists {
		grant.Lease.Complete(false)
		writeOpenAIError(w, http.StatusInternalServerError, "gateway_error", "selected worker has no backend handler")
		return
	}
	w.Header().Set("X-GoLLM-Worker-ID", grant.Worker.ID)
	w.Header().Set(headers.QueueWaitMS, strconv.FormatInt(grant.QueueWait.Milliseconds(), 10))
	w.Header().Set(headers.SchedulerReason, grant.Reason)
	w.Header().Set(headers.WorkerExpectedLoad, strconv.FormatFloat(grant.ExpectedLoad, 'f', 6, 64))
	outcome := &proxyOutcome{}
	r = r.WithContext(withProxyOutcome(r.Context(), outcome))
	tracked := &statusResponseWriter{ResponseWriter: w}
	handler.ServeHTTP(tracked, r)
	grant.Lease.Complete(r.Context().Err() == nil && tracked.Status() >= http.StatusOK && tracked.Status() < http.StatusMultipleChoices && !outcome.failed.Load())
}

func (p *Pool) logRejected(metadata coremodel.RequestMetadata, request scheduler.Request, err error) {
	reason := "scheduler_error"
	var queueWait time.Duration
	var schedulerError *scheduler.Error
	if errors.As(err, &schedulerError) {
		reason = schedulerError.Code
		queueWait = schedulerError.QueueWait
	} else if errors.Is(err, context.Canceled) {
		reason = "request_canceled"
	} else if errors.Is(err, context.DeadlineExceeded) {
		reason = "deadline_exceeded"
	}
	p.logger.Warn("request rejected by scheduler",
		"request_id", metadata.ID, "model", metadata.Model,
		"workload_class", metadata.WorkloadClass, "task_kind", metadata.TaskKind,
		"request_cost", request.Cost, "queue_wait_ms", queueWait.Milliseconds(),
		"deadline_remaining_ms", deadlineRemainingMS(request.Deadline),
		"scheduler_reason", reason,
	)
}

func deadlineRemainingMS(deadline time.Time) int64 {
	if deadline.IsZero() {
		return -1
	}
	remaining := time.Until(deadline)
	if remaining < 0 {
		return 0
	}
	return remaining.Milliseconds()
}

func (p *Pool) deadline(ctx context.Context, metadata coremodel.RequestMetadata) time.Time {
	var deadline time.Time
	slo := metadata.CompletionDeadline
	if slo <= 0 {
		slo = p.classSLO[string(metadata.WorkloadClass)]
	}
	if slo > 0 {
		deadline = metadata.ReceivedAt.Add(slo)
	}
	if contextDeadline, ok := ctx.Deadline(); ok && (deadline.IsZero() || contextDeadline.Before(deadline)) {
		deadline = contextDeadline
	}
	return deadline
}

func (p *Pool) writeMetadataHeaders(header http.Header, metadata coremodel.RequestMetadata) {
	header.Set(headers.RequestID, metadata.ID)
	header.Set(headers.WorkloadClass, string(metadata.WorkloadClass))
	header.Set(headers.WorkloadClassSource, string(metadata.ClassSource))
	header.Set(headers.TaskKind, string(metadata.TaskKind))
	header.Set(headers.TaskSource, string(metadata.TaskSource))
}
func (p *Pool) writeProcessingError(w http.ResponseWriter, err error, internalMessage string) {
	var requestError *coremodel.RequestError
	if errors.As(err, &requestError) {
		writeOpenAIError(w, http.StatusBadRequest, requestError.ErrorCode, requestError.Message)
		return
	}
	writeOpenAIError(w, http.StatusInternalServerError, "gateway_error", internalMessage)
}

func (p *Pool) writeSchedulerError(w http.ResponseWriter, err error) {
	if contextErr := contextError(err); contextErr != nil {
		status, code, message := upstreamError(contextErr)
		writeOpenAIError(w, status, code, message)
		return
	}
	var schedulerError *scheduler.Error
	if errors.As(err, &schedulerError) {
		status := http.StatusTooManyRequests
		if schedulerError.Code == "no_available_worker" || schedulerError.Code == "gateway_shutting_down" {
			status = http.StatusServiceUnavailable
		}
		if schedulerError.RetryAfter > 0 {
			seconds := int64(math.Ceil(schedulerError.RetryAfter.Seconds()))
			if seconds < 1 {
				seconds = 1
			}
			w.Header().Set("Retry-After", strconv.FormatInt(seconds, 10))
		}
		writeOpenAIError(w, status, schedulerError.Code, schedulerError.Message)
		return
	}
	writeOpenAIError(w, http.StatusInternalServerError, "gateway_error", "scheduler dispatch failed")
}

func contextError(err error) error {
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return nil
}

func (p *Pool) readRequestBody(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	body := http.MaxBytesReader(w, r.Body, p.maxBodyBytes)
	data, err := io.ReadAll(body)
	_ = body.Close()
	if err == nil {
		return data, nil
	}
	var maxBytesError *http.MaxBytesError
	if errors.As(err, &maxBytesError) {
		writeOpenAIError(w, http.StatusRequestEntityTooLarge, "request_too_large", "request body exceeds the configured limit")
	} else {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "failed to read request body")
	}
	return nil, err
}

func (p *Pool) serveModels(w http.ResponseWriter) {
	models := make([]map[string]any, 0, len(p.models))
	for _, model := range p.models {
		if len(p.availability.Available(model)) == 0 {
			continue
		}
		models = append(models, map[string]any{"id": model, "object": "model", "owned_by": "gollm-gateway"})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": models})
}

type statusResponseWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusResponseWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}
func (w *statusResponseWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(data)
}
func (w *statusResponseWriter) Flush() {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}
func (w *statusResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *statusResponseWriter) Status() int {
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}

var _ http.Flusher = (*statusResponseWriter)(nil)
