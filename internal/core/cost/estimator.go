package cost

import (
	"fmt"
	"maps"

	coremodel "github.com/lshan02508-ai/GOLLM/internal/core/model"
)

type EstimatorOptions struct {
	BytesPerToken              int64
	MessageOverheadTokens      int64
	DefaultContextWindowTokens int64
	DefaultOutputTokens        int64
	TaskOutputTokens           map[coremodel.TaskKind]int64
	WorkloadOutputTokens       map[coremodel.WorkloadClass]int64
	ModelContextWindowTokens   map[string]int64
}

type Estimator struct {
	options EstimatorOptions
}

func NewEstimator(options EstimatorOptions) (*Estimator, error) {
	if options.BytesPerToken <= 0 || options.MessageOverheadTokens < 0 || options.DefaultContextWindowTokens <= 0 || options.DefaultOutputTokens <= 0 {
		return nil, fmt.Errorf("token budget defaults are invalid")
	}
	for kind, tokens := range options.TaskOutputTokens {
		if _, ok := coremodel.ParseTaskKind(string(kind)); !ok || tokens <= 0 {
			return nil, fmt.Errorf("invalid output budget for task %q", kind)
		}
	}
	for class, tokens := range options.WorkloadOutputTokens {
		if _, ok := coremodel.ParseWorkloadClass(string(class)); !ok || tokens <= 0 {
			return nil, fmt.Errorf("invalid output budget for workload %q", class)
		}
	}
	for model, tokens := range options.ModelContextWindowTokens {
		if model == "" || tokens <= 0 {
			return nil, fmt.Errorf("invalid context window for model %q", model)
		}
	}
	options.TaskOutputTokens = maps.Clone(options.TaskOutputTokens)
	options.WorkloadOutputTokens = maps.Clone(options.WorkloadOutputTokens)
	options.ModelContextWindowTokens = maps.Clone(options.ModelContextWindowTokens)
	return &Estimator{options: options}, nil
}

func (e *Estimator) Estimate(metadata coremodel.RequestMetadata) (TokenBudget, error) {
	promptTokens := ceilDiv(metadata.PromptBytes, e.options.BytesPerToken) + int64(metadata.MessageCount)*e.options.MessageOverheadTokens
	if promptTokens < 1 {
		promptTokens = 1
	}
	contextWindow := e.options.DefaultContextWindowTokens
	if configured := e.options.ModelContextWindowTokens[metadata.Model]; configured > 0 {
		contextWindow = configured
	}
	hardOutputLimit := contextWindow - promptTokens
	if hardOutputLimit <= 0 {
		return TokenBudget{}, requestError("context_length_exceeded", "estimated prompt tokens exceed the model context window")
	}

	expected, source := e.defaultOutput(metadata)
	reserved := expected
	if metadata.MaxOutputTokens > 0 {
		reserved = metadata.MaxOutputTokens
		source = BudgetSourceClientLimit
		if expected > reserved {
			expected = reserved
		}
	}
	if reserved > hardOutputLimit {
		if metadata.MaxOutputTokens > 0 {
			return TokenBudget{}, requestError("context_length_exceeded", "estimated prompt tokens plus the requested output exceed the model context window")
		}
		reserved = hardOutputLimit
		if expected > reserved {
			expected = reserved
		}
	}
	return TokenBudget{
		PromptTokensEstimated: promptTokens, ExpectedOutputTokens: expected,
		ReservedOutputTokens: reserved, TotalReservedTokens: promptTokens + reserved,
		HardOutputLimit: hardOutputLimit, ContextWindowTokens: contextWindow,
		Method: EstimationBytesFallback, Source: source,
	}, nil
}

func (e *Estimator) defaultOutput(metadata coremodel.RequestMetadata) (int64, BudgetSource) {
	if metadata.TaskKind != coremodel.TaskUnknown {
		if tokens := e.options.TaskOutputTokens[metadata.TaskKind]; tokens > 0 {
			return tokens, BudgetSourceTaskDefault
		}
	}
	if metadata.ClassSource != coremodel.ClassSourceClientHint {
		if tokens := e.options.WorkloadOutputTokens[metadata.WorkloadClass]; tokens > 0 {
			return tokens, BudgetSourceWorkloadDefault
		}
	}
	return e.options.DefaultOutputTokens, BudgetSourceSystemDefault
}

func ceilDiv(value, divisor int64) int64 {
	if value <= 0 {
		return 0
	}
	return 1 + (value-1)/divisor
}

func requestError(code, message string) error {
	return &coremodel.RequestError{ErrorCode: code, Message: message}
}
