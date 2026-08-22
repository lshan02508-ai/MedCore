package cost

import "context"

type EstimationMethod string

const EstimationBytesFallback EstimationMethod = "bytes_fallback"

type BudgetSource string

const (
	BudgetSourceClientLimit     BudgetSource = "client_limit"
	BudgetSourceTaskDefault     BudgetSource = "task_default"
	BudgetSourceWorkloadDefault BudgetSource = "workload_default"
	BudgetSourceSystemDefault   BudgetSource = "system_default"
)

// TokenBudget separates expected use from the policy reservation consumed by
// admission control. It can later be reconciled with backend usage data.
type TokenBudget struct {
	PromptTokensEstimated int64
	ExpectedOutputTokens  int64
	ReservedOutputTokens  int64
	TotalReservedTokens   int64
	HardOutputLimit       int64
	ContextWindowTokens   int64
	Method                EstimationMethod
	Source                BudgetSource
}

type tokenBudgetKey struct{}

func WithTokenBudget(ctx context.Context, budget TokenBudget) context.Context {
	return context.WithValue(ctx, tokenBudgetKey{}, budget)
}

func TokenBudgetFromContext(ctx context.Context) (TokenBudget, bool) {
	budget, ok := ctx.Value(tokenBudgetKey{}).(TokenBudget)
	return budget, ok
}
