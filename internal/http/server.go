package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"time"

	"github.com/PolarishT/sales-agent/biz/model/health"
	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/middlewares/server/recovery"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"github.com/google/uuid"
)

const (
	defaultReadinessTimeout = 2 * time.Second
	defaultRequestTimeout   = 30 * time.Second
	defaultShutdownTimeout  = 10 * time.Second

	multipartRequestOverheadBytes = int64(1 << 20)
	defaultMaxRequestBodySize     = 6 << 20
)

type Options struct {
	Address            string
	RequestTimeout     time.Duration
	ShutdownTimeout    time.Duration
	MaxRequestBodySize int
	Listener           net.Listener
	Dependencies       Dependencies
}

func NewServer(options Options) *server.Hertz {
	requestTimeout := options.RequestTimeout
	if requestTimeout <= 0 {
		requestTimeout = defaultRequestTimeout
	}
	shutdownTimeout := options.ShutdownTimeout
	if shutdownTimeout <= 0 {
		shutdownTimeout = defaultShutdownTimeout
	}
	if options.Dependencies.ReadinessTimeout <= 0 {
		options.Dependencies.ReadinessTimeout = defaultReadinessTimeout
	}
	maxRequestBodySize := options.MaxRequestBodySize
	if maxRequestBodySize <= 0 {
		maxRequestBodySize = defaultMaxRequestBodySize
	}

	listenOption := server.WithHostPorts(options.Address)
	if options.Listener != nil {
		listenOption = server.WithListener(options.Listener)
	}
	h := server.New(
		listenOption,
		server.WithReadTimeout(requestTimeout),
		server.WithWriteTimeout(requestTimeout),
		server.WithExitWaitTime(shutdownTimeout),
		server.WithMaxRequestBodySize(maxRequestBodySize),
		server.WithHandleMethodNotAllowed(true),
	)
	h.Use(
		requestIDMiddleware(),
		dependenciesMiddleware(options.Dependencies),
		recovery.Recovery(recovery.WithRecoveryHandler(recoveryHandler)),
	)
	h.NoRoute(func(_ context.Context, ctx *app.RequestContext) {
		WriteError(ctx, consts.StatusNotFound, "error", "NOT_FOUND", "请求的资源不存在")
	})
	h.NoMethod(func(_ context.Context, ctx *app.RequestContext) {
		WriteError(ctx, consts.StatusMethodNotAllowed, "error", "METHOD_NOT_ALLOWED", "请求方法不受支持")
	})
	return h
}

func MultipartRequestBodySize(maxUploadBytes int64) (int, error) {
	if maxUploadBytes <= 0 {
		return 0, errors.New("RAG 上传大小上限必须大于 0")
	}
	maxInt := int64(^uint(0) >> 1)
	if maxUploadBytes > maxInt-multipartRequestOverheadBytes {
		return 0, errors.New("RAG 上传大小上限无法转换为 Hertz 请求体上限")
	}
	return int(maxUploadBytes + multipartRequestOverheadBytes), nil
}

func WriteError(ctx *app.RequestContext, statusCode int, status, code, message string) {
	ctx.JSON(statusCode, &health.ErrorResponse{
		Status:    status,
		Code:      code,
		Message:   message,
		RequestID: ctx.GetString("request_id"),
	})
}

func requestIDMiddleware() app.HandlerFunc {
	return func(ctx context.Context, requestContext *app.RequestContext) {
		requestID := uuid.NewString()
		requestContext.Set("request_id", requestID)
		requestContext.Response.Header.Set("X-Request-ID", requestID)
		requestContext.Next(ctx)
	}
}

func recoveryHandler(_ context.Context, ctx *app.RequestContext, _ any, _ []byte) {
	slog.Error("请求处理发生 panic", "request_id", ctx.GetString("request_id"))
	ctx.Abort()
	WriteError(ctx, consts.StatusInternalServerError, "error", "INTERNAL_ERROR", "服务内部错误")
}
