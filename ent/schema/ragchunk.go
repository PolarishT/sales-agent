package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/pgvector/pgvector-go"
)

// RagChunk holds a searchable chunk belonging to a document version.
type RagChunk struct {
	ent.Schema
}

// Fields of the RagChunk.
func (RagChunk) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("id"),
		field.Int64("document_version_id"),
		field.Int("chunk_index"),
		field.Text("content"),
		field.Text("embedding_content"),
		field.JSON("heading_path", []string{}).
			Default([]string{}).
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}),
		field.Int("start_line"),
		field.Int("end_line"),
		field.Int("estimated_tokens"),
		field.String("content_hash").SchemaType(map[string]string{dialect.Postgres: "char(64)"}),
		field.Other("embedding", pgvector.Vector{}).
			SchemaType(map[string]string{dialect.Postgres: "vector(1024)"}),
		field.Time("created_at").
			Default(time.Now).
			Immutable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

// Edges of the RagChunk.
func (RagChunk) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("document_version", RagDocumentVersion.Type).
			Ref("chunks").
			Field("document_version_id").
			Unique().
			Required(),
	}
}

// Indexes of the RagChunk.
func (RagChunk) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("document_version_id"),
		index.Fields("document_version_id", "chunk_index").Unique(),
	}
}

// Annotations fixes the entity to the externally managed PostgreSQL table name.
func (RagChunk) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "rag_chunks"}}
}
