package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	pgxvector "github.com/pgvector/pgvector-go/pgx"
)

type Options struct {
	MaxConns        int32
	MinConns        int32
	MaxConnIdleTime time.Duration
}

// NewPool 创建连接池，但不会主动发起数据库连接。
// 数据库可用性由 HTTP readiness 检查在受限超时内判断。
func NewPool(ctx context.Context, databaseURL string, options Options) (*pgxpool.Pool, error) {
	config, err := parseConfig(databaseURL, options)
	if err != nil {
		return nil, err
	}
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("创建 PostgreSQL 连接池: %w", err)
	}
	return pool, nil
}

func parseConfig(databaseURL string, options Options) (*pgxpool.Config, error) {
	if strings.TrimSpace(databaseURL) == "" {
		return nil, errors.New("DATABASE_URL 不能为空")
	}
	if options.MaxConns <= 0 {
		return nil, errors.New("数据库最大连接数必须大于 0")
	}
	if options.MinConns < 0 || options.MinConns > options.MaxConns {
		return nil, errors.New("数据库最小连接数必须位于 0 和最大连接数之间")
	}
	if options.MaxConnIdleTime < 0 {
		return nil, errors.New("数据库连接最大空闲时间不能为负数")
	}
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, errors.New("DATABASE_URL 格式无效")
	}
	config.MaxConns = options.MaxConns
	config.MinConns = options.MinConns
	config.MaxConnIdleTime = options.MaxConnIdleTime
	config.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		if err := pgxvector.RegisterTypes(ctx, conn); err != nil {
			return fmt.Errorf("注册 pgvector 类型: %w", err)
		}
		return nil
	}
	return config, nil
}
