package config

import (
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	coremodel "github.com/lshan02508-ai/GOLLM/internal/core/model"
)

type Config struct {
	Server                 ServerConfig                 `json:"server" yaml:"server"`
	Backend                BackendConfig                `json:"backend" yaml:"backend"`
	WorkerHealth           WorkerHealthConfig           `json:"worker_health" yaml:"worker_health"`
	WorkloadClassification WorkloadClassificationConfig `json:"workload_classification" yaml:"workload_classification"`
	TokenBudget            TokenBudgetConfig            `json:"token_budget" yaml:"token_budget"`
	Scheduler              SchedulerConfig              `json:"scheduler" yaml:"scheduler"`
	ModelPools             map[string]ModelPoolConfig   `json:"model_pools" yaml:"model_pools"`
}

type ServerConfig struct {
	Listen              string `json:"listen" yaml:"listen"`
	MaxRequestBodyBytes int64  `json:"max_request_body_bytes" yaml:"max_request_body_bytes"`
	ReadHeaderTimeout   string `json:"read_header_timeout" yaml:"read_header_timeout"`
	RequestTimeout      string `json:"request_timeout" yaml:"request_timeout"`
	ShutdownTimeout     string `json:"shutdown_timeout" yaml:"shutdown_timeout"`
}

type BackendConfig struct {
	BaseURL               string `json:"base_url" yaml:"base_url"`
	APIKey                string `json:"api_key" yaml:"api_key"`
	ReadinessTimeout      string `json:"readiness_timeout" yaml:"readiness_timeout"`
	FirstTokenTimeout     string `json:"first_token_timeout" yaml:"first_token_timeout"`
	StreamIdleTimeout     string `json:"stream_idle_timeout" yaml:"stream_idle_timeout"`
	ResponseHeaderTimeout string `json:"response_header_timeout" yaml:"response_header_timeout"`
	IdleConnTimeout       string `json:"idle_conn_timeout" yaml:"idle_conn_timeout"`
	MaxIdleConns          int    `json:"max_idle_conns" yaml:"max_idle_conns"`
	MaxIdleConnsPerHost   int    `json:"max_idle_conns_per_host" yaml:"max_idle_conns_per_host"`
}

type ModelPoolConfig struct {
	Model           string         `json:"model" yaml:"model"`
	MaxInflightCost int64          `json:"max_inflight_cost" yaml:"max_inflight_cost"`
	Workers         []WorkerConfig `json:"workers" yaml:"workers"`
}

type WorkerConfig struct {
	ID                 string  `json:"id" yaml:"id"`
	Endpoint           string  `json:"endpoint" yaml:"endpoint"`
	APIKey             string  `json:"api_key" yaml:"api_key"`
	MaxConcurrency     int64   `json:"max_concurrency" yaml:"max_concurrency"`
	MaxInflightCost    int64   `json:"max_inflight_cost" yaml:"max_inflight_cost"`
	InitialServiceRate float64 `json:"initial_service_rate" yaml:"initial_service_rate"`
}

type WorkerHealthConfig struct {
	Interval           string `json:"interval" yaml:"interval"`
	Timeout            string `json:"timeout" yaml:"timeout"`
	UnhealthyThreshold int    `json:"unhealthy_threshold" yaml:"unhealthy_threshold"`
	HealthyThreshold   int    `json:"healthy_threshold" yaml:"healthy_threshold"`
}

type WorkloadClassificationConfig struct {
	AllowClientHint          *bool `json:"allow_client_hint" yaml:"allow_client_hint"`
	RealtimeMaxInputBytes    int64 `json:"realtime_max_input_bytes" yaml:"realtime_max_input_bytes"`
	RealtimeMaxOutputTokens  int64 `json:"realtime_max_output_tokens" yaml:"realtime_max_output_tokens"`
	LongContextMinInputBytes int64 `json:"long_context_min_input_bytes" yaml:"long_context_min_input_bytes"`
	BatchMinOutputTokens     int64 `json:"batch_min_output_tokens" yaml:"batch_min_output_tokens"`
}

type TokenBudgetConfig struct {
	AllowClientTaskHint        *bool            `json:"allow_client_task_hint" yaml:"allow_client_task_hint"`
	BytesPerToken              int64            `json:"bytes_per_token" yaml:"bytes_per_token"`
	MessageOverheadTokens      int64            `json:"message_overhead_tokens" yaml:"message_overhead_tokens"`
	DefaultContextWindowTokens int64            `json:"default_context_window_tokens" yaml:"default_context_window_tokens"`
	DefaultOutputTokens        int64            `json:"default_output_tokens" yaml:"default_output_tokens"`
	TaskOutputTokens           map[string]int64 `json:"task_output_tokens" yaml:"task_output_tokens"`
	WorkloadOutputTokens       map[string]int64 `json:"workload_output_tokens" yaml:"workload_output_tokens"`
	ModelContextWindowTokens   map[string]int64 `json:"model_context_window_tokens" yaml:"model_context_window_tokens"`
}

type SchedulerConfig struct {
	QueueCapacity  int               `json:"queue_capacity" yaml:"queue_capacity"`
	MaxQueuedCost  int64             `json:"max_queued_cost" yaml:"max_queued_cost"`
	MaxQueueWait   string            `json:"max_queue_wait" yaml:"max_queue_wait"`
	RetryAfter     string            `json:"retry_after" yaml:"retry_after"`
	AgingInterval  string            `json:"aging_interval" yaml:"aging_interval"`
	MaxBypass      int               `json:"max_bypass" yaml:"max_bypass"`
	PrefillWeight  int64             `json:"prefill_weight" yaml:"prefill_weight"`
	DecodeWeight   int64             `json:"decode_weight" yaml:"decode_weight"`
	EWMAAlpha      float64           `json:"ewma_alpha" yaml:"ewma_alpha"`
	MinServiceRate float64           `json:"min_service_rate" yaml:"min_service_rate"`
	MaxServiceRate float64           `json:"max_service_rate" yaml:"max_service_rate"`
	ClassSLO       map[string]string `json:"class_slo" yaml:"class_slo"`
}

type RuntimeWorker struct {
	ID                 string
	Endpoint           string
	APIKey             string
	Models             []string
	MaxConcurrency     int64
	MaxInflightCost    int64
	InitialServiceRate float64
}

type RuntimeModelPool struct {
	Model           string
	MaxInflightCost int64
}

type Runtime struct {
	Config
	ReadHeaderTimeout      time.Duration
	RequestTimeout         time.Duration
	ShutdownTimeout        time.Duration
	ReadinessTimeout       time.Duration
	FirstTokenTimeout      time.Duration
	StreamIdleTimeout      time.Duration
	ResponseHeaderTimeout  time.Duration
	IdleConnTimeout        time.Duration
	WorkerHealthInterval   time.Duration
	WorkerHealthTimeout    time.Duration
	SchedulerMaxQueueWait  time.Duration
	SchedulerRetryAfter    time.Duration
	SchedulerAgingInterval time.Duration
	SchedulerClassSLO      map[string]time.Duration
	Workers                []RuntimeWorker
	RuntimeModelPools      []RuntimeModelPool
	ExplicitModelPools     bool
}

func (c Config) Validate() (Runtime, error) {
	if c.Server.Listen == "" {
		c.Server.Listen = ":8080"
	}
	if c.Server.MaxRequestBodyBytes == 0 {
		c.Server.MaxRequestBodyBytes = 32 << 20
	}
	if c.Server.MaxRequestBodyBytes < 1 {
		return Runtime{}, errors.New("server.max_request_body_bytes must be positive")
	}
	hasLegacyBackend := strings.TrimSpace(c.Backend.BaseURL) != ""
	hasModelPools := len(c.ModelPools) != 0
	if hasLegacyBackend == hasModelPools {
		return Runtime{}, errors.New("configure exactly one of backend.base_url or model_pools")
	}
	c.Server.ReadHeaderTimeout = defaultString(c.Server.ReadHeaderTimeout, "5s")
	c.Server.RequestTimeout = defaultString(c.Server.RequestTimeout, "10m")
	c.Server.ShutdownTimeout = defaultString(c.Server.ShutdownTimeout, "30s")
	c.Backend.ReadinessTimeout = defaultString(c.Backend.ReadinessTimeout, "2s")
	c.Backend.FirstTokenTimeout = defaultString(c.Backend.FirstTokenTimeout, "30s")
	c.Backend.StreamIdleTimeout = defaultString(c.Backend.StreamIdleTimeout, "30s")
	c.Backend.ResponseHeaderTimeout = defaultString(c.Backend.ResponseHeaderTimeout, "30s")
	c.Backend.IdleConnTimeout = defaultString(c.Backend.IdleConnTimeout, "90s")
	c.WorkerHealth.Interval = defaultString(c.WorkerHealth.Interval, "5s")
	c.WorkerHealth.Timeout = defaultString(c.WorkerHealth.Timeout, "2s")
	if c.WorkerHealth.UnhealthyThreshold == 0 {
		c.WorkerHealth.UnhealthyThreshold = 3
	}
	if c.WorkerHealth.HealthyThreshold == 0 {
		c.WorkerHealth.HealthyThreshold = 2
	}
	if c.WorkerHealth.UnhealthyThreshold < 1 || c.WorkerHealth.HealthyThreshold < 1 {
		return Runtime{}, errors.New("worker_health thresholds must be positive")
	}
	if c.WorkloadClassification.AllowClientHint == nil {
		allow := false
		c.WorkloadClassification.AllowClientHint = &allow
	}
	if c.WorkloadClassification.RealtimeMaxInputBytes == 0 {
		c.WorkloadClassification.RealtimeMaxInputBytes = 4 << 10
	}
	if c.WorkloadClassification.RealtimeMaxOutputTokens == 0 {
		c.WorkloadClassification.RealtimeMaxOutputTokens = 256
	}
	if c.WorkloadClassification.LongContextMinInputBytes == 0 {
		c.WorkloadClassification.LongContextMinInputBytes = 32 << 10
	}
	if c.WorkloadClassification.BatchMinOutputTokens == 0 {
		c.WorkloadClassification.BatchMinOutputTokens = 2048
	}
	classification := c.WorkloadClassification
	if classification.RealtimeMaxInputBytes < 1 || classification.RealtimeMaxOutputTokens < 1 || classification.LongContextMinInputBytes < 1 || classification.BatchMinOutputTokens < 1 {
		return Runtime{}, errors.New("workload_classification thresholds must be positive")
	}
	if classification.LongContextMinInputBytes <= classification.RealtimeMaxInputBytes {
		return Runtime{}, errors.New("workload_classification.long_context_min_input_bytes must exceed realtime_max_input_bytes")
	}
	if classification.BatchMinOutputTokens <= classification.RealtimeMaxOutputTokens {
		return Runtime{}, errors.New("workload_classification.batch_min_output_tokens must exceed realtime_max_output_tokens")
	}
	if err := defaultAndValidateTokenBudget(&c.TokenBudget); err != nil {
		return Runtime{}, err
	}
	classSLO, err := defaultAndValidateScheduler(&c.Scheduler)
	if err != nil {
		return Runtime{}, err
	}
	if c.Backend.MaxIdleConns == 0 {
		c.Backend.MaxIdleConns = 100
	}
	if c.Backend.MaxIdleConnsPerHost == 0 {
		c.Backend.MaxIdleConnsPerHost = 100
	}

	readHeader, err := positiveDuration("server.read_header_timeout", c.Server.ReadHeaderTimeout)
	if err != nil {
		return Runtime{}, err
	}
	request, err := positiveDuration("server.request_timeout", c.Server.RequestTimeout)
	if err != nil {
		return Runtime{}, err
	}
	shutdown, err := positiveDuration("server.shutdown_timeout", c.Server.ShutdownTimeout)
	if err != nil {
		return Runtime{}, err
	}
	readiness, err := positiveDuration("backend.readiness_timeout", c.Backend.ReadinessTimeout)
	if err != nil {
		return Runtime{}, err
	}
	firstToken, err := positiveDuration("backend.first_token_timeout", c.Backend.FirstTokenTimeout)
	if err != nil {
		return Runtime{}, err
	}
	streamIdle, err := positiveDuration("backend.stream_idle_timeout", c.Backend.StreamIdleTimeout)
	if err != nil {
		return Runtime{}, err
	}
	responseHeader, err := positiveDuration("backend.response_header_timeout", c.Backend.ResponseHeaderTimeout)
	if err != nil {
		return Runtime{}, err
	}
	idle, err := positiveDuration("backend.idle_conn_timeout", c.Backend.IdleConnTimeout)
	if err != nil {
		return Runtime{}, err
	}
	healthInterval, err := positiveDuration("worker_health.interval", c.WorkerHealth.Interval)
	if err != nil {
		return Runtime{}, err
	}
	healthTimeout, err := positiveDuration("worker_health.timeout", c.WorkerHealth.Timeout)
	if err != nil {
		return Runtime{}, err
	}
	if healthTimeout >= healthInterval {
		return Runtime{}, errors.New("worker_health.timeout must be shorter than worker_health.interval")
	}

	workers, pools, err := c.runtimeWorkers(hasLegacyBackend)
	if err != nil {
		return Runtime{}, err
	}
	c.Backend.BaseURL = strings.TrimRight(c.Backend.BaseURL, "/")
	maxQueueWait, _ := time.ParseDuration(c.Scheduler.MaxQueueWait)
	retryAfter, _ := time.ParseDuration(c.Scheduler.RetryAfter)
	agingInterval, _ := time.ParseDuration(c.Scheduler.AgingInterval)
	return Runtime{Config: c, ReadHeaderTimeout: readHeader, RequestTimeout: request, ShutdownTimeout: shutdown, ReadinessTimeout: readiness, FirstTokenTimeout: firstToken, StreamIdleTimeout: streamIdle, ResponseHeaderTimeout: responseHeader, IdleConnTimeout: idle, WorkerHealthInterval: healthInterval, WorkerHealthTimeout: healthTimeout, SchedulerMaxQueueWait: maxQueueWait, SchedulerRetryAfter: retryAfter, SchedulerAgingInterval: agingInterval, SchedulerClassSLO: classSLO, Workers: workers, RuntimeModelPools: pools, ExplicitModelPools: hasModelPools}, nil
}

func defaultAndValidateScheduler(scheduler *SchedulerConfig) (map[string]time.Duration, error) {
	if scheduler.QueueCapacity == 0 {
		scheduler.QueueCapacity = 500
	}
	if scheduler.MaxQueuedCost == 0 {
		scheduler.MaxQueuedCost = 500000
	}
	scheduler.MaxQueueWait = defaultString(scheduler.MaxQueueWait, "5s")
	scheduler.RetryAfter = defaultString(scheduler.RetryAfter, "1s")
	scheduler.AgingInterval = defaultString(scheduler.AgingInterval, "30s")
	if scheduler.MaxBypass == 0 {
		scheduler.MaxBypass = 8
	}
	if scheduler.PrefillWeight == 0 {
		scheduler.PrefillWeight = 1
	}
	if scheduler.DecodeWeight == 0 {
		scheduler.DecodeWeight = 3
	}
	if scheduler.EWMAAlpha == 0 {
		scheduler.EWMAAlpha = 0.2
	}
	if scheduler.MinServiceRate == 0 {
		scheduler.MinServiceRate = 100
	}
	if scheduler.MaxServiceRate == 0 {
		scheduler.MaxServiceRate = 1000000
	}
	if scheduler.QueueCapacity < 1 || scheduler.MaxQueuedCost < 1 || scheduler.MaxBypass < 1 || scheduler.PrefillWeight < 1 || scheduler.DecodeWeight < 1 {
		return nil, errors.New("scheduler capacities, bypass, and cost weights must be positive")
	}
	if scheduler.EWMAAlpha <= 0 || scheduler.EWMAAlpha > 1 || scheduler.MinServiceRate <= 0 || scheduler.MaxServiceRate < scheduler.MinServiceRate {
		return nil, errors.New("scheduler service rate settings are invalid")
	}
	for name, value := range map[string]string{"scheduler.max_queue_wait": scheduler.MaxQueueWait, "scheduler.retry_after": scheduler.RetryAfter, "scheduler.aging_interval": scheduler.AgingInterval} {
		if _, err := positiveDuration(name, value); err != nil {
			return nil, err
		}
	}
	defaults := map[string]string{"realtime": "800ms", "interactive": "3s", "long_context": "10s", "batch": ""}
	for key, value := range scheduler.ClassSLO {
		defaults[key] = value
	}
	classSLO := make(map[string]time.Duration, len(defaults))
	for class, value := range defaults {
		if _, ok := coremodel.ParseWorkloadClass(class); !ok {
			return nil, fmt.Errorf("scheduler.class_slo.%s is unsupported", class)
		}
		if value == "" || value == "0" || value == "0s" {
			classSLO[class] = 0
			continue
		}
		d, err := positiveDuration("scheduler.class_slo."+class, value)
		if err != nil {
			return nil, err
		}
		classSLO[class] = d
	}
	scheduler.ClassSLO = defaults
	return classSLO, nil
}

func defaultAndValidateTokenBudget(budget *TokenBudgetConfig) error {
	if budget.AllowClientTaskHint == nil {
		allow := false
		budget.AllowClientTaskHint = &allow
	}
	if budget.BytesPerToken == 0 {
		budget.BytesPerToken = 3
	}
	if budget.MessageOverheadTokens == 0 {
		budget.MessageOverheadTokens = 4
	}
	if budget.DefaultContextWindowTokens == 0 {
		budget.DefaultContextWindowTokens = 32768
	}
	if budget.DefaultOutputTokens == 0 {
		budget.DefaultOutputTokens = 1024
	}
	if budget.BytesPerToken < 1 || budget.MessageOverheadTokens < 0 || budget.DefaultContextWindowTokens < 1 || budget.DefaultOutputTokens < 1 {
		return errors.New("token_budget numeric defaults are invalid")
	}
	budget.TaskOutputTokens = mergeTokenDefaults(map[string]int64{
		"qa": 256, "extraction": 512, "summarization": 1024,
		"code_generation": 2048, "reasoning": 4096, "tool_use": 1024,
	}, budget.TaskOutputTokens)
	budget.WorkloadOutputTokens = mergeTokenDefaults(map[string]int64{
		"realtime": 256, "interactive": 1024, "long_context": 1024, "batch": 4096,
	}, budget.WorkloadOutputTokens)
	for kind, tokens := range budget.TaskOutputTokens {
		if _, ok := coremodel.ParseTaskKind(kind); !ok || tokens <= 0 {
			return fmt.Errorf("token_budget.task_output_tokens.%s must be a positive supported task kind", kind)
		}
	}
	for class, tokens := range budget.WorkloadOutputTokens {
		if _, ok := coremodel.ParseWorkloadClass(class); !ok || tokens <= 0 {
			return fmt.Errorf("token_budget.workload_output_tokens.%s must be a positive supported workload class", class)
		}
	}
	for model, tokens := range budget.ModelContextWindowTokens {
		if strings.TrimSpace(model) == "" || tokens <= 0 {
			return fmt.Errorf("token_budget.model_context_window_tokens.%s must be positive", model)
		}
	}
	return nil
}

func mergeTokenDefaults(defaults, configured map[string]int64) map[string]int64 {
	merged := make(map[string]int64, len(defaults)+len(configured))
	for key, value := range defaults {
		merged[key] = value
	}
	for key, value := range configured {
		merged[key] = value
	}
	return merged
}

func (c Config) runtimeWorkers(hasLegacyBackend bool) ([]RuntimeWorker, []RuntimeModelPool, error) {
	if hasLegacyBackend {
		endpoint, err := validWorkerEndpoint("backend.base_url", c.Backend.BaseURL)
		if err != nil {
			return nil, nil, err
		}
		return []RuntimeWorker{{ID: "default", Endpoint: endpoint, APIKey: c.Backend.APIKey, Models: []string{"*"}, MaxInflightCost: 50000, InitialServiceRate: 3000}}, []RuntimeModelPool{{Model: "*", MaxInflightCost: 100000}}, nil
	}

	poolNames := make([]string, 0, len(c.ModelPools))
	for name := range c.ModelPools {
		poolNames = append(poolNames, name)
	}
	sort.Strings(poolNames)
	seenWorkers := make(map[string]struct{})
	workers := make([]RuntimeWorker, 0)
	pools := make([]RuntimeModelPool, 0, len(poolNames))
	for _, poolName := range poolNames {
		if strings.TrimSpace(poolName) == "" {
			return nil, nil, errors.New("model pool name must not be empty")
		}
		pool := c.ModelPools[poolName]
		pool.Model = strings.TrimSpace(pool.Model)
		if pool.Model == "" {
			return nil, nil, fmt.Errorf("model_pools.%s.model is required", poolName)
		}
		if len(pool.Workers) == 0 {
			return nil, nil, fmt.Errorf("model_pools.%s.workers must not be empty", poolName)
		}
		if pool.MaxInflightCost == 0 {
			pool.MaxInflightCost = 100000
		}
		if pool.MaxInflightCost < 1 {
			return nil, nil, fmt.Errorf("model_pools.%s.max_inflight_cost must be positive", poolName)
		}
		pools = append(pools, RuntimeModelPool{Model: pool.Model, MaxInflightCost: pool.MaxInflightCost})
		for index, worker := range pool.Workers {
			worker.ID = strings.TrimSpace(worker.ID)
			worker.Endpoint = strings.TrimRight(strings.TrimSpace(worker.Endpoint), "/")
			if worker.ID == "" {
				return nil, nil, fmt.Errorf("model_pools.%s.workers[%d].id is required", poolName, index)
			}
			if _, exists := seenWorkers[worker.ID]; exists {
				return nil, nil, fmt.Errorf("worker ID %q is duplicated", worker.ID)
			}
			if worker.MaxConcurrency < 0 {
				return nil, nil, fmt.Errorf("model_pools.%s.workers[%d].max_concurrency must not be negative", poolName, index)
			}
			if worker.MaxInflightCost == 0 {
				worker.MaxInflightCost = 50000
			}
			if worker.InitialServiceRate == 0 {
				worker.InitialServiceRate = 3000
			}
			if worker.MaxInflightCost < 1 || worker.InitialServiceRate <= 0 {
				return nil, nil, fmt.Errorf("model_pools.%s.workers[%d] cost and service rate must be positive", poolName, index)
			}
			endpoint, err := validWorkerEndpoint(fmt.Sprintf("model_pools.%s.workers[%d].endpoint", poolName, index), worker.Endpoint)
			if err != nil {
				return nil, nil, err
			}
			seenWorkers[worker.ID] = struct{}{}
			apiKey := worker.APIKey
			if apiKey == "" {
				apiKey = c.Backend.APIKey
			}
			workers = append(workers, RuntimeWorker{ID: worker.ID, Endpoint: endpoint, APIKey: apiKey, Models: []string{pool.Model}, MaxConcurrency: worker.MaxConcurrency, MaxInflightCost: worker.MaxInflightCost, InitialServiceRate: worker.InitialServiceRate})
		}
	}
	return workers, pools, nil
}

func validWorkerEndpoint(name, value string) (string, error) {
	endpoint, err := url.Parse(strings.TrimSpace(value))
	if err != nil || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.Host == "" {
		return "", fmt.Errorf("%s must be a valid HTTP(S) URL: %q", name, value)
	}
	if endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return "", fmt.Errorf("%s must not contain credentials, query, or fragment", name)
	}
	return strings.TrimRight(endpoint.String(), "/"), nil
}

func positiveDuration(name, value string) (time.Duration, error) {
	d, err := time.ParseDuration(value)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("%s must be a positive duration: %q", name, value)
	}
	return d, nil
}

func defaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
