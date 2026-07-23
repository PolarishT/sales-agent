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
	"github.com/google/uuid"
)

// RagDocumentVersion holds one immutable source upload and its ingestion state.
type RagDocumentVersion struct {
	ent.Schema
}

// Fields of the RagDocumentVersion.
func (RagDocumentVersion) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("id"),
		field.UUID("ingestion_id", uuid.UUID{}).Unique(),
		field.Int64("document_id"),
		field.Int("version"),
		field.String("file_name").MaxLen(255),
		field.String("content_hash").SchemaType(map[string]string{dialect.Postgres: "char(64)"}),
		field.Text("original_markdown"),
		field.Int64("source_bytes"),
		field.String("status").MaxLen(16),
		field.String("stage").MaxLen(24),
		field.Int("chunk_count").Default(0),
		field.Int("embedded_chunk_count").Default(0),
		field.String("failure_code").MaxLen(64).Optional().Nillable(),
		field.String("failure_message").MaxLen(255).Optional().Nillable(),
		field.Time("created_at").
			Default(time.Now).
			Immutable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("updated_at").
			Default(time.Now).
			UpdateDefault(time.Now).
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("completed_at").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

// Edges of the RagDocumentVersion.
func (RagDocumentVersion) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("document", RagDocument.Type).
			Ref("versions").
			Field("document_id").
			Unique().
			Required(),
		edge.To("chunks", RagChunk.Type).
			Annotations(entsql.OnDelete(entsql.Cascade)),
	}
}

// Indexes of the RagDocumentVersion.
func (RagDocumentVersion) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("document_id"),
		index.Fields("document_id", "version").Unique(),
	}
}

// Annotations fixes the entity to the externally managed PostgreSQL table name.
func (RagDocumentVersion) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "rag_document_versions"}}
}
