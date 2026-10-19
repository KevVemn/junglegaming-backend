package httpapi

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type SQSProbe interface {
	Ping(ctx context.Context) error
}

type HealthChecker struct {
	pool *pgxpool.Pool
	sqs  SQSProbe
}

func NewHealthChecker(pool *pgxpool.Pool, sqs SQSProbe) *HealthChecker {
	return &HealthChecker{pool: pool, sqs: sqs}
}

func (h *HealthChecker) Check(ctx context.Context) (map[string]string, bool) {
	checks := make(map[string]string, 2)
	healthy := true

	pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	if err := h.pool.Ping(pingCtx); err != nil {
		checks["postgres"] = "down: " + err.Error()
		healthy = false
	} else {
		checks["postgres"] = "up"
	}

	if h.sqs == nil {
		checks["sqs"] = "disabled"
		return checks, healthy
	}

	if err := h.sqs.Ping(pingCtx); err != nil {
		checks["sqs"] = "down: " + err.Error()
		healthy = false
	} else {
		checks["sqs"] = "up"
	}

	return checks, healthy
}
