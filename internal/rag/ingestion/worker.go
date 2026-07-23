package ingestion

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/PolarishT/sales-agent/internal/rag/domain"
	"github.com/google/uuid"
)

const maxFailurePersistenceTimeout = 5 * time.Second

func (e *Executor) worker() {
	defer e.wg.Done()
	for {
		if e.ctx.Err() != nil {
			e.drainInterrupted()
			return
		}
		select {
		case ingestionID := <-e.jobs:
			if e.ctx.Err() != nil {
				e.persistFailureSafely(ingestionID, domain.StageQueued, interruptedFailure())
				e.finishJob()
				e.drainInterrupted()
				return
			}
			e.executeAtBoundary(ingestionID)
		case <-e.ctx.Done():
			e.drainInterrupted()
			return
		}
	}
}

func (e *Executor) executeAtBoundary(ingestionID uuid.UUID) {
	defer func() {
		if recover() != nil {
			e.persistFailureSafely(
				ingestionID,
				domain.StageQueued,
				domain.Failure{
					Code:    domain.CodeInternalProcessing,
					Message: failureMessage(domain.CodeInternalProcessing),
				},
			)
		}
	}()
	e.execute(ingestionID)
}

func (e *Executor) execute(ingestionID uuid.UUID) {
	defer e.finishJob()
	startedAt := time.Now()
	taskCtx, cancel := context.WithTimeout(e.ctx, e.taskTimeout)
	err := runSafely(taskCtx, e.runner, ingestionID)
	taskContextErr := taskCtx.Err()
	if err == nil {
		err = taskContextErr
	}
	cancel()
	if err == nil {
		slog.Info(
			"RAG 导入任务完成",
			"ingestion_id", ingestionID,
			"status", domain.StatusSucceeded,
			"duration", time.Since(startedAt),
		)
		return
	}

	failure := classifyFailure(err, taskContextErr)
	task := e.persistFailure(ingestionID, stageFromError(err), failure)
	attributes := []any{
		"ingestion_id", ingestionID,
		"status", domain.StatusFailed,
		"stage", task.Stage,
		"duration", time.Since(startedAt),
	}
	if task.DocumentKey != "" {
		attributes = append(attributes, "document_key", task.DocumentKey)
	}
	if task.ChunkCount > 0 {
		attributes = append(attributes, "chunk_count", task.ChunkCount)
	}
	if task.EmbeddedChunkCount > 0 {
		attributes = append(attributes, "embedded_chunk_count", task.EmbeddedChunkCount)
	}
	attributes = append(attributes, "code", failure.Code)
	slog.Error("RAG 导入任务失败", attributes...)
}

func (e *Executor) drainInterrupted() {
	for {
		select {
		case ingestionID := <-e.jobs:
			e.persistFailureSafely(ingestionID, domain.StageQueued, interruptedFailure())
			e.finishJob()
		default:
			return
		}
	}
}

func runSafely(ctx context.Context, runner Runner, ingestionID uuid.UUID) (err error) {
	defer func() {
		if recover() != nil {
			err = domain.NewError(
				domain.CodeInternalProcessing,
				failureMessage(domain.CodeInternalProcessing),
				nil,
			)
		}
	}()
	return runner.Run(ctx, ingestionID)
}

func (e *Executor) persistFailure(
	ingestionID uuid.UUID,
	fallbackStage domain.Stage,
	failure domain.Failure,
) domain.Task {
	timeout := e.taskTimeout
	if timeout > maxFailurePersistenceTimeout {
		timeout = maxFailurePersistenceTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	task, err := e.repository.GetTask(ctx, ingestionID)
	if err != nil || task.Stage == "" {
		task.Stage = fallbackStage
	}
	if err := e.repository.MarkFailed(ctx, ingestionID, task.Stage, failure); err != nil {
		slog.Error(
			"记录 RAG 导入失败状态失败",
			"ingestion_id", ingestionID,
			"status", domain.StatusFailed,
			"stage", task.Stage,
			"code", failure.Code,
		)
	}
	return task
}

func (e *Executor) persistFailureSafely(
	ingestionID uuid.UUID,
	fallbackStage domain.Stage,
	failure domain.Failure,
) {
	defer func() {
		_ = recover()
	}()
	e.persistFailure(ingestionID, fallbackStage, failure)
}

func classifyFailure(err error, taskContextErr error) domain.Failure {
	if taskContextErr != nil {
		return domain.Failure{
			Code:    domain.CodeProcessInterrupted,
			Message: failureMessage(domain.CodeProcessInterrupted),
		}
	}
	var stable *domain.Error
	if errors.As(err, &stable) && isProcessingFailureCode(stable.Code) {
		return domain.Failure{
			Code:    stable.Code,
			Message: failureMessage(stable.Code),
		}
	}
	return domain.Failure{
		Code:    domain.CodeInternalProcessing,
		Message: failureMessage(domain.CodeInternalProcessing),
	}
}

func failureMessage(code string) string {
	switch code {
	case domain.CodeMarkdownParseFailed:
		return "Markdown 解析失败"
	case domain.CodeNoIndexableContent:
		return "文档没有可索引内容"
	case domain.CodeDocumentSplitFailed:
		return "文档切分失败"
	case domain.CodeEmbeddingFailed:
		return "文本向量生成失败"
	case domain.CodeInvalidEmbeddingResponse:
		return "Embedding 响应无效"
	case domain.CodeDocumentStoreFailed:
		return "文档存储失败"
	case domain.CodeProcessInterrupted:
		return "文档导入处理已中断"
	case domain.CodeInternalProcessing:
		return "文档导入处理失败"
	default:
		return "文档导入处理失败"
	}
}

func interruptedFailure() domain.Failure {
	return domain.Failure{
		Code:    domain.CodeProcessInterrupted,
		Message: failureMessage(domain.CodeProcessInterrupted),
	}
}

func isProcessingFailureCode(code string) bool {
	switch code {
	case domain.CodeMarkdownParseFailed,
		domain.CodeNoIndexableContent,
		domain.CodeDocumentSplitFailed,
		domain.CodeEmbeddingFailed,
		domain.CodeInvalidEmbeddingResponse,
		domain.CodeDocumentStoreFailed,
		domain.CodeProcessInterrupted,
		domain.CodeInternalProcessing:
		return true
	default:
		return false
	}
}
