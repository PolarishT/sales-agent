package agent

import (
	"context"
	"testing"
)

func TestSkeletonGraphInvoke(t *testing.T) {
	runner, err := NewGraph(context.Background())
	if err != nil {
		t.Fatalf("NewGraph() error = %v", err)
	}

	response, err := runner.Invoke(context.Background(), Request{Query: "  推荐一款敏感肌护肤品  "})
	if err != nil {
		t.Fatalf("Invoke() error = %v", err)
	}
	if response.Query != "推荐一款敏感肌护肤品" {
		t.Fatalf("Query = %q, want trimmed query", response.Query)
	}
	if response.Stage != "skeleton" {
		t.Fatalf("Stage = %q, want skeleton", response.Stage)
	}
}

func TestSkeletonGraphRejectsEmptyQuery(t *testing.T) {
	runner, err := NewGraph(context.Background())
	if err != nil {
		t.Fatalf("NewGraph() error = %v", err)
	}

	_, err = runner.Invoke(context.Background(), Request{Query: "   "})
	if err == nil {
		t.Fatal("Invoke() error = nil, want an error")
	}
}
