package database

import (
	"context"
	"errors"

	"github.com/PolarishT/sales-agent/ent"
)

type existsFunc func(context.Context) (bool, error)

// Readiness checks that the existing rag_users table is queryable through Ent.
type Readiness struct {
	exists existsFunc
}

// NewReadiness creates a readiness checker backed by an Ent Client.
func NewReadiness(client *ent.Client) *Readiness {
	if client == nil {
		return &Readiness{}
	}
	return &Readiness{exists: func(ctx context.Context) (bool, error) {
		return client.RagUser.Query().Exist(ctx)
	}}
}

// Ping reports whether Ent can query the existing rag_users table.
func (readiness *Readiness) Ping(ctx context.Context) error {
	if readiness == nil || readiness.exists == nil {
		return errors.New("缺少 Ent 数据库客户端")
	}
	_, err := readiness.exists(ctx)
	return err
}
