package app

import (
	"context"
	"errors"

	"github.com/lshan02508-ai/GOLLM/internal/registry"
)

type readinessRegistry interface {
	Available(string) []registry.WorkerSnapshot
}

type poolReadiness struct {
	registry readinessRegistry
}

func (p *poolReadiness) Ready(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(p.registry.Available("")) == 0 {
		return errors.New("no healthy inference workers")
	}
	return nil
}
