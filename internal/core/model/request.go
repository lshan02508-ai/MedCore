package model

import (
	"context"
	"fmt"
	"time"
)

// WorkloadClass is the stable scheduling class derived at the ingress edge.
type WorkloadClass string

const (
	WorkloadRealtime    WorkloadClass = "realtime"
	WorkloadInteractive WorkloadClass = "interactive"
	WorkloadLongContext WorkloadClass = "long_context"
	WorkloadBatch       WorkloadClass = "batch"
)

func ParseWorkloadClass(value string) (WorkloadClass, bool) {
	class := WorkloadClass(value)
	switch class {
	case WorkloadRealtime, WorkloadInteractive, WorkloadLongContext, WorkloadBatch:
		return class, true
	default:
		return "", false
	}
}

type ClassSource string

const (
	ClassSourceAutomatic  ClassSource = "automatic"
	ClassSourceClientHint ClassSource = "client_hint"
)

type TaskKind string

const (
	TaskUnknown       TaskKind = "unknown"
	TaskQA            TaskKind = "qa"
	TaskExtraction    TaskKind = "extraction"
	TaskSummarization TaskKind = "summarization"
	TaskCode          TaskKind = "code_generation"
	TaskReasoning     TaskKind = "reasoning"
	TaskToolUse       TaskKind = "tool_use"
)

func ParseTaskKind(value string) (TaskKind, bool) {
	kind := TaskKind(value)
	switch kind {
	case TaskUnknown, TaskQA, TaskExtraction, TaskSummarization, TaskCode, TaskReasoning, TaskToolUse:
		return kind, true
	default:
		return "", false
	}
}

type TaskSource string

const (
	TaskSourceAutomatic  TaskSource = "automatic"
	TaskSourceClientHint TaskSource = "client_hint"
)

// RequestMetadata is the normalized, backend-independent representation used
// by admission, scheduling, accounting, and observability layers.
type RequestMetadata struct {
	ID                 string
	ReceivedAt         time.Time
	Model              string
	Stream             bool
	WorkloadClass      WorkloadClass
	ClassSource        ClassSource
	TaskKind           TaskKind
	TaskSource         TaskSource
	MessageCount       int
	ToolCount          int
	InputBytes         int64
	PromptBytes        int64
	MaxOutputTokens    int64
	TenantHint         string
	ProjectHint        string
	UserHint           string
	FirstTokenSLO      time.Duration
	CompletionDeadline time.Duration
}

// RequestError is safe to expose to an API client as an OpenAI-style 400.
type RequestError struct {
	ErrorCode string
	Message   string
}

func (e *RequestError) Error() string {
	return fmt.Sprintf("%s: %s", e.ErrorCode, e.Message)
}

type requestMetadataKey struct{}

func WithRequestMetadata(ctx context.Context, metadata RequestMetadata) context.Context {
	return context.WithValue(ctx, requestMetadataKey{}, metadata)
}

func RequestMetadataFromContext(ctx context.Context) (RequestMetadata, bool) {
	metadata, ok := ctx.Value(requestMetadataKey{}).(RequestMetadata)
	return metadata, ok
}
