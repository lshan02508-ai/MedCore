package scheduler

import (
	"errors"
	"math"

	"github.com/lshan02508-ai/GOLLM/internal/core/cost"
)

func CalculateRequestCost(budget cost.TokenBudget, prefillWeight, decodeWeight int64) (int64, error) {
	if budget.PromptTokensEstimated < 0 || budget.ReservedOutputTokens < 0 || prefillWeight <= 0 || decodeWeight <= 0 {
		return 0, errors.New("token budget and cost weights must be positive")
	}
	if budget.PromptTokensEstimated > math.MaxInt64/prefillWeight || budget.ReservedOutputTokens > math.MaxInt64/decodeWeight {
		return 0, errors.New("request cost overflow")
	}
	prompt := budget.PromptTokensEstimated * prefillWeight
	output := budget.ReservedOutputTokens * decodeWeight
	if prompt > math.MaxInt64-output {
		return 0, errors.New("request cost overflow")
	}
	total := prompt + output
	if total <= 0 {
		return 0, errors.New("request cost must be positive")
	}
	return total, nil
}
