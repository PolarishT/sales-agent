package ingestion

import (
	"context"
	"errors"

	"github.com/PolarishT/sales-agent/internal/rag/domain"
	"github.com/google/uuid"
)

type API interface {
	Submit(context.Context, string, string, []byte) (domain.Submission, error)
	Get(context.Context, uuid.UUID) (domain.Task, error)
}

type Service struct {
	repository Repository
	executor   *Executor
	maxBytes   int64
}

func NewService(
	repository Repository,
	executor *Executor,
	maxBytes int64,
) (*Service, error) {
	if repository == nil {
		return nil, errors.New("缺少 RAG 导入仓储")
	}
	if executor == nil {
		return nil, errors.New("缺少 RAG 导入执行器")
	}
	if maxBytes <= 0 {
		return nil, errors.New("RAG 上传大小上限必须大于 0")
	}
	return &Service{
		repository: repository,
		executor:   executor,
		maxBytes:   maxBytes,
	}, nil
}

func (s *Service) Submit(
	ctx context.Context,
	documentKey string,
	fileName string,
	raw []byte,
) (domain.Submission, error) {
	validatedKey, err := domain.ValidateDocumentKey(documentKey)
	if err != nil {
		return domain.Submission{}, err
	}
	upload, err := domain.NormalizeUpload(fileName, raw, s.maxBytes)
	if err != nil {
		return domain.Submission{}, err
	}
	upload.DocumentKey = validatedKey

	task, decision, err := s.repository.InspectSubmission(ctx, validatedKey, upload.ContentHash)
	if err != nil {
		return domain.Submission{}, ingestionUnavailable(err)
	}
	switch decision {
	case SubmissionReuse:
		return s.reuseSubmission(task)
	case SubmissionConflict:
		return domain.Submission{}, ingestionConflict()
	case SubmissionCreate:
	default:
		return domain.Submission{}, domain.NewError(
			domain.CodeInternalProcessing,
			"导入提交决策无效",
			nil,
		)
	}

	reservation, ok := s.executor.TryReserve()
	if !ok {
		if !s.executor.accepting() {
			return domain.Submission{}, domain.NewError(
				domain.CodeIngestionUnavailable,
				"文档导入服务暂不可用",
				nil,
			)
		}
		return domain.Submission{}, domain.NewError(
			domain.CodeIngestionQueueFull,
			"文档导入队列已满",
			nil,
		)
	}

	task, decision, err = s.repository.CreateQueued(ctx, upload)
	if err != nil {
		reservation.Release()
		return domain.Submission{}, ingestionUnavailable(err)
	}
	switch decision {
	case SubmissionReuse:
		if task.Status == domain.StatusQueued {
			if !reservation.Commit(task.IngestionID) {
				return domain.Submission{}, ingestionUnavailable(nil)
			}
			return domain.Submission{Task: task, Deduplicated: true}, nil
		}
		reservation.Release()
		return s.reuseSubmission(task)
	case SubmissionConflict:
		reservation.Release()
		return domain.Submission{}, ingestionConflict()
	case SubmissionCreate:
		if !reservation.Commit(task.IngestionID) {
			s.executor.persistFailureSafely(
				task.IngestionID,
				domain.StageQueued,
				interruptedFailure(),
			)
			return domain.Submission{}, domain.NewError(
				domain.CodeIngestionUnavailable,
				"文档导入服务暂不可用",
				nil,
			)
		}
		return domain.Submission{Task: task}, nil
	default:
		reservation.Release()
		return domain.Submission{}, domain.NewError(
			domain.CodeInternalProcessing,
			"导入创建决策无效",
			nil,
		)
	}
}

func (s *Service) reuseSubmission(task domain.Task) (domain.Submission, error) {
	switch task.Status {
	case domain.StatusSucceeded:
		return domain.Submission{Task: task, Deduplicated: true}, nil
	case domain.StatusRunning:
		if s.executor.IsScheduled(task.IngestionID) {
			return domain.Submission{Task: task, Deduplicated: true}, nil
		}
		return domain.Submission{}, ingestionUnavailable(nil)
	case domain.StatusQueued:
		switch s.executor.ensureScheduled(task.IngestionID) {
		case scheduleAccepted, scheduleAlreadyPresent:
			return domain.Submission{Task: task, Deduplicated: true}, nil
		case scheduleFull:
			return domain.Submission{}, domain.NewError(
				domain.CodeIngestionQueueFull,
				"文档导入队列已满",
				nil,
			)
		case scheduleUnavailable:
			return domain.Submission{}, ingestionUnavailable(nil)
		default:
			return domain.Submission{}, domain.NewError(
				domain.CodeInternalProcessing,
				"导入调度结果无效",
				nil,
			)
		}
	default:
		return domain.Submission{}, ingestionUnavailable(nil)
	}
}

func (s *Service) Get(ctx context.Context, ingestionID uuid.UUID) (domain.Task, error) {
	task, err := s.repository.GetTask(ctx, ingestionID)
	if err == nil || domain.IsCode(err, domain.CodeIngestionNotFound) {
		return task, err
	}
	return domain.Task{}, ingestionUnavailable(err)
}

func (e *Executor) accepting() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.started && !e.stopping && !e.aborted && e.ctx.Err() == nil
}

func ingestionConflict() error {
	return domain.NewError(
		domain.CodeIngestionInProgress,
		"相同 document_key 已有不同内容正在导入",
		nil,
	)
}

func ingestionUnavailable(err error) error {
	return domain.NewError(
		domain.CodeIngestionUnavailable,
		"文档导入服务暂不可用",
		err,
	)
}

var _ API = (*Service)(nil)
