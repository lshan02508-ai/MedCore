package openai

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	coremodel "github.com/lshan02508-ai/GOLLM/internal/core/model"
	"github.com/lshan02508-ai/GOLLM/pkg/headers"
)

const maxIdentityHintBytes = 256

type NormalizerOptions struct {
	AllowClientClassHint     bool
	AllowClientTaskHint      bool
	RealtimeMaxInputBytes    int64
	RealtimeMaxOutputTokens  int64
	LongContextMinInputBytes int64
	BatchMinOutputTokens     int64
	Now                      func() time.Time
	NewRequestID             func() (string, error)
}

type Normalizer struct {
	options NormalizerOptions
}

func NewNormalizer(options NormalizerOptions) (*Normalizer, error) {
	if options.RealtimeMaxInputBytes <= 0 || options.RealtimeMaxOutputTokens <= 0 || options.LongContextMinInputBytes <= 0 || options.BatchMinOutputTokens <= 0 {
		return nil, fmt.Errorf("workload classification thresholds must be positive")
	}
	if options.LongContextMinInputBytes <= options.RealtimeMaxInputBytes {
		return nil, fmt.Errorf("long-context input threshold must exceed the realtime input threshold")
	}
	if options.BatchMinOutputTokens <= options.RealtimeMaxOutputTokens {
		return nil, fmt.Errorf("batch output threshold must exceed the realtime output threshold")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.NewRequestID == nil {
		options.NewRequestID = randomRequestID
	}
	return &Normalizer{options: options}, nil
}

type chatCompletionRequest struct {
	Model               string            `json:"model"`
	Messages            []json.RawMessage `json:"messages"`
	Stream              bool              `json:"stream"`
	MaxTokens           *int64            `json:"max_tokens"`
	MaxCompletionTokens *int64            `json:"max_completion_tokens"`
	User                string            `json:"user"`
	Tools               []json.RawMessage `json:"tools"`
	ResponseFormat      json.RawMessage   `json:"response_format"`
}

func (n *Normalizer) Normalize(body []byte, requestHeaders http.Header) (coremodel.RequestMetadata, error) {
	var request chatCompletionRequest
	if err := json.Unmarshal(body, &request); err != nil {
		return coremodel.RequestMetadata{}, invalid("invalid_request_error", "request body must be valid JSON")
	}
	model := strings.TrimSpace(request.Model)
	if model == "" {
		return coremodel.RequestMetadata{}, invalid("invalid_request_error", "model is required")
	}
	if model != request.Model {
		return coremodel.RequestMetadata{}, invalid("invalid_request_error", "model must not contain leading or trailing whitespace")
	}
	maxOutput, err := outputLimit(request)
	if err != nil {
		return coremodel.RequestMetadata{}, err
	}
	class, source, err := n.classify(requestHeaders.Get(headers.WorkloadClass), int64(len(body)), maxOutput, request.Stream)
	if err != nil {
		return coremodel.RequestMetadata{}, err
	}
	taskKind, taskSource, err := n.taskProfile(requestHeaders.Get(headers.TaskKind), request)
	if err != nil {
		return coremodel.RequestMetadata{}, err
	}
	firstTokenSLO, err := durationHint(requestHeaders, headers.SLOFirstTokenMS)
	if err != nil {
		return coremodel.RequestMetadata{}, err
	}
	deadline, err := durationHint(requestHeaders, headers.SLODeadlineMS)
	if err != nil {
		return coremodel.RequestMetadata{}, err
	}
	tenant, err := identityHint(requestHeaders, headers.TenantID)
	if err != nil {
		return coremodel.RequestMetadata{}, err
	}
	project, err := identityHint(requestHeaders, headers.ProjectID)
	if err != nil {
		return coremodel.RequestMetadata{}, err
	}
	requestID, err := n.options.NewRequestID()
	if err != nil {
		return coremodel.RequestMetadata{}, fmt.Errorf("generate request ID: %w", err)
	}
	return coremodel.RequestMetadata{
		ID: requestID, ReceivedAt: n.options.Now(), Model: model, Stream: request.Stream,
		WorkloadClass: class, ClassSource: source, TaskKind: taskKind, TaskSource: taskSource,
		MessageCount: len(request.Messages), ToolCount: len(request.Tools), InputBytes: int64(len(body)),
		PromptBytes: promptBytes(request), MaxOutputTokens: maxOutput, TenantHint: tenant,
		ProjectHint: project, UserHint: strings.TrimSpace(request.User), FirstTokenSLO: firstTokenSLO,
		CompletionDeadline: deadline,
	}, nil
}

func (n *Normalizer) taskProfile(hint string, request chatCompletionRequest) (coremodel.TaskKind, coremodel.TaskSource, error) {
	hint = strings.TrimSpace(strings.ToLower(hint))
	if hint != "" && n.options.AllowClientTaskHint {
		kind, ok := coremodel.ParseTaskKind(hint)
		if !ok {
			return "", "", invalid("invalid_task_kind", "task kind must be one of unknown, qa, extraction, summarization, code_generation, reasoning, or tool_use")
		}
		return kind, coremodel.TaskSourceClientHint, nil
	}
	if len(request.Tools) > 0 {
		return coremodel.TaskToolUse, coremodel.TaskSourceAutomatic, nil
	}
	if len(request.ResponseFormat) > 0 && string(request.ResponseFormat) != "null" {
		var format struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(request.ResponseFormat, &format); err != nil {
			return "", "", invalid("invalid_request_error", "response_format must be a valid object")
		}
		if format.Type == "json_object" || format.Type == "json_schema" {
			return coremodel.TaskExtraction, coremodel.TaskSourceAutomatic, nil
		}
	}
	return coremodel.TaskUnknown, coremodel.TaskSourceAutomatic, nil
}

func promptBytes(request chatCompletionRequest) int64 {
	var total int64
	for _, message := range request.Messages {
		total += int64(len(message))
	}
	for _, tool := range request.Tools {
		total += int64(len(tool))
	}
	total += int64(len(request.ResponseFormat))
	return total
}

func (n *Normalizer) classify(hint string, inputBytes, maxOutputTokens int64, stream bool) (coremodel.WorkloadClass, coremodel.ClassSource, error) {
	hint = strings.TrimSpace(strings.ToLower(hint))
	if hint != "" && n.options.AllowClientClassHint {
		class, ok := coremodel.ParseWorkloadClass(hint)
		if !ok {
			return "", "", invalid("invalid_workload_class", "workload class must be one of realtime, interactive, long_context, or batch")
		}
		return class, coremodel.ClassSourceClientHint, nil
	}
	if inputBytes >= n.options.LongContextMinInputBytes {
		return coremodel.WorkloadLongContext, coremodel.ClassSourceAutomatic, nil
	}
	if maxOutputTokens >= n.options.BatchMinOutputTokens {
		return coremodel.WorkloadBatch, coremodel.ClassSourceAutomatic, nil
	}
	if stream && inputBytes <= n.options.RealtimeMaxInputBytes && (maxOutputTokens == 0 || maxOutputTokens <= n.options.RealtimeMaxOutputTokens) {
		return coremodel.WorkloadRealtime, coremodel.ClassSourceAutomatic, nil
	}
	return coremodel.WorkloadInteractive, coremodel.ClassSourceAutomatic, nil
}

func outputLimit(request chatCompletionRequest) (int64, error) {
	limit := request.MaxTokens
	if request.MaxCompletionTokens != nil {
		limit = request.MaxCompletionTokens
	}
	if limit == nil {
		return 0, nil
	}
	if *limit < 0 {
		return 0, invalid("invalid_request_error", "max_tokens and max_completion_tokens must not be negative")
	}
	return *limit, nil
}

func durationHint(h http.Header, name string) (time.Duration, error) {
	value := strings.TrimSpace(h.Get(name))
	if value == "" {
		return 0, nil
	}
	milliseconds, err := strconv.ParseInt(value, 10, 64)
	if err != nil || milliseconds <= 0 || milliseconds > int64((time.Duration(1<<63-1))/time.Millisecond) {
		return 0, invalid("invalid_slo_hint", name+" must be a positive integer number of milliseconds")
	}
	return time.Duration(milliseconds) * time.Millisecond, nil
}

func identityHint(h http.Header, name string) (string, error) {
	value := strings.TrimSpace(h.Get(name))
	if len(value) > maxIdentityHintBytes {
		return "", invalid("invalid_identity_hint", name+" must not exceed 256 bytes")
	}
	return value, nil
}

func invalid(code, message string) error {
	return &coremodel.RequestError{ErrorCode: code, Message: message}
}

func randomRequestID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return "req_" + hex.EncodeToString(value[:]), nil
}
