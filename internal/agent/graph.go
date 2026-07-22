package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/compose"
)

const normalizeNode = "normalize_query"

type Request struct {
	Query string `json:"query"`
}

type Response struct {
	Query string `json:"query"`
	Stage string `json:"stage"`
}

type Runner struct {
	runnable compose.Runnable[Request, Response]
}

func NewGraph(ctx context.Context) (*Runner, error) {
	graph := compose.NewGraph[Request, Response]()
	node := compose.InvokableLambda(func(_ context.Context, request Request) (Response, error) {
		query := strings.TrimSpace(request.Query)
		if query == "" {
			return Response{}, errors.New("query 不能为空")
		}
		return Response{Query: query, Stage: "skeleton"}, nil
	})
	if err := graph.AddLambdaNode(normalizeNode, node); err != nil {
		return nil, fmt.Errorf("添加 Graph 节点 %q: %w", normalizeNode, err)
	}
	if err := graph.AddEdge(compose.START, normalizeNode); err != nil {
		return nil, fmt.Errorf("连接 Graph 起始节点: %w", err)
	}
	if err := graph.AddEdge(normalizeNode, compose.END); err != nil {
		return nil, fmt.Errorf("连接 Graph 结束节点: %w", err)
	}
	runnable, err := graph.Compile(ctx, compose.WithGraphName("shopping_guide"))
	if err != nil {
		return nil, fmt.Errorf("编译导购 Graph: %w", err)
	}
	return &Runner{runnable: runnable}, nil
}

func (r *Runner) Invoke(ctx context.Context, request Request) (Response, error) {
	if r == nil || r.runnable == nil {
		return Response{}, errors.New("导购 Graph 未初始化")
	}
	return r.runnable.Invoke(ctx, request)
}
