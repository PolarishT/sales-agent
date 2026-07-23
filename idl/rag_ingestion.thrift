namespace go rag_ingestion

// CreateIngestion consumes multipart/form-data.
// The form field document_key is declared below.
// The transport must also contain exactly one file field named "file".
// The file must end in .md or .markdown and must not exceed 5 MiB.
struct CreateIngestionRequest {
    1: required string DocumentKey (
        api.form="document_key",
        go.tag="json:\"document_key\""
    )
}

struct CreateIngestionResponse {
    1: required string IngestionID (go.tag="json:\"ingestion_id\"")
    2: required string DocumentKey (go.tag="json:\"document_key\"")
    3: required string Status (go.tag="json:\"status\"")
    4: required string Stage (go.tag="json:\"stage\"")
    5: required bool Deduplicated (go.tag="json:\"deduplicated\"")
    6: required string CreatedAt (go.tag="json:\"created_at\"")
}

struct GetIngestionRequest {
    1: required string IngestionID (
        api.path="ingestion_id",
        go.tag="json:\"ingestion_id\""
    )
}

struct IngestionFailure {
    1: required string Code (go.tag="json:\"code\"")
    2: required string Message (go.tag="json:\"message\"")
}

struct GetIngestionResponse {
    1: required string IngestionID (go.tag="json:\"ingestion_id\"")
    2: required string DocumentKey (go.tag="json:\"document_key\"")
    3: required string Status (go.tag="json:\"status\"")
    4: required string Stage (go.tag="json:\"stage\"")
    5: required i64 SourceBytes (go.tag="json:\"source_bytes\"")
    6: required i32 ChunkCount (go.tag="json:\"chunk_count\"")
    7: required i32 EmbeddedChunkCount (go.tag="json:\"embedded_chunk_count\"")
    8: required string CreatedAt (go.tag="json:\"created_at\"")
    9: required string UpdatedAt (go.tag="json:\"updated_at\"")
    10: optional string CompletedAt (go.tag="json:\"completed_at,omitempty\"")
    11: optional IngestionFailure Failure (go.tag="json:\"failure,omitempty\"")
}

service RAGIngestionService {
    CreateIngestionResponse CreateIngestion(
        1: CreateIngestionRequest request
    ) (api.post="/api/v1/rag/ingestions")

    GetIngestionResponse GetIngestion(
        1: GetIngestionRequest request
    ) (api.get="/api/v1/rag/ingestions/:ingestion_id")
}
