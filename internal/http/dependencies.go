package httpapi

import (
	"context"
	"time"

	"github.com/PolarishT/sales-agent/internal/agent"
	"github.com/PolarishT/sales-agent/internal/rag/ingestion"
	"github.com/cloudwego/hertz/pkg/app"
)

const dependenciesKey = "application_dependencies"

type HealthChecker interface {
	Ping(context.Context) error
}

type AgentRunner interface {
	Invoke(context.Context, agent.Request) (agent.Response, error)
}

type Dependencies struct {
	HealthChecker    HealthChecker
	AgentRunner      AgentRunner
	IngestionService ingestion.API
	MaxUploadBytes   int64
	ReadinessTimeout time.Duration
}

func DependenciesFrom(ctx *app.RequestContext) (Dependencies, bool) {
	value, ok := ctx.Get(dependenciesKey)
	if !ok {
		return Dependencies{}, false
	}
	dependencies, ok := value.(Dependencies)
	return dependencies, ok
}

func dependenciesMiddleware(dependencies Dependencies) app.HandlerFunc {
	return func(ctx context.Context, requestContext *app.RequestContext) {
		requestContext.Set(dependenciesKey, dependencies)
		requestContext.Next(ctx)
	}
}
