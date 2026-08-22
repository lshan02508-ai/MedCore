package headers

const (
	RequestID           = "X-GoLLM-Request-ID"
	WorkloadClass       = "X-GoLLM-Workload-Class"
	WorkloadClassSource = "X-GoLLM-Workload-Class-Source"
	SLOFirstTokenMS     = "X-GoLLM-SLO-TTFT-Ms"
	SLODeadlineMS       = "X-GoLLM-SLO-Deadline-Ms"
	TenantID            = "X-GoLLM-Tenant-ID"
	ProjectID           = "X-GoLLM-Project-ID"
	TaskKind            = "X-GoLLM-Task-Kind"
	TaskSource          = "X-GoLLM-Task-Source"
	PromptTokens        = "X-GoLLM-Prompt-Tokens-Estimated"
	ReservedTokens      = "X-GoLLM-Reserved-Tokens"
	BudgetSource        = "X-GoLLM-Token-Budget-Source"
	RequestCost         = "X-GoLLM-Request-Cost"
	QueueWaitMS         = "X-GoLLM-Queue-Wait-Ms"
	SchedulerReason     = "X-GoLLM-Scheduler-Reason"
	WorkerExpectedLoad  = "X-GoLLM-Worker-Expected-Load"
)
