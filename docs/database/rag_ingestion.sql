CREATE EXTENSION IF NOT EXISTS vector;

CREATE TABLE IF NOT EXISTS rag_documents (
    id BIGSERIAL PRIMARY KEY,
    document_key VARCHAR(128) UNIQUE NOT NULL,
    current_version INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS rag_document_versions (
    id BIGSERIAL PRIMARY KEY,
    ingestion_id UUID UNIQUE NOT NULL,
    document_id BIGINT NOT NULL REFERENCES rag_documents(id) ON DELETE CASCADE,
    version INTEGER NOT NULL,
    file_name VARCHAR(255) NOT NULL,
    content_hash CHAR(64) NOT NULL,
    original_markdown TEXT NOT NULL,
    source_bytes BIGINT NOT NULL,
    status VARCHAR(16) NOT NULL,
    stage VARCHAR(24) NOT NULL,
    chunk_count INTEGER NOT NULL DEFAULT 0,
    embedded_chunk_count INTEGER NOT NULL DEFAULT 0,
    failure_code VARCHAR(64),
    failure_message VARCHAR(255),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ,
    UNIQUE (document_id, version)
);

CREATE TABLE IF NOT EXISTS rag_chunks (
    id BIGSERIAL PRIMARY KEY,
    document_version_id BIGINT NOT NULL REFERENCES rag_document_versions(id) ON DELETE CASCADE,
    chunk_index INTEGER NOT NULL,
    content TEXT NOT NULL,
    embedding_content TEXT NOT NULL,
    heading_path JSONB NOT NULL DEFAULT '[]'::jsonb,
    start_line INTEGER NOT NULL,
    end_line INTEGER NOT NULL,
    estimated_tokens INTEGER NOT NULL,
    content_hash CHAR(64) NOT NULL,
    embedding VECTOR(1024) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (document_version_id, chunk_index)
);

CREATE INDEX IF NOT EXISTS rag_document_versions_document_id_idx
    ON rag_document_versions (document_id);

CREATE INDEX IF NOT EXISTS rag_chunks_document_version_id_idx
    ON rag_chunks (document_version_id);
