package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

// RagDocument holds the stable identity and current version of an ingested document.
type RagDocument struct {
	ent.Schema
}

// Fields of the RagDocument.
func (RagDocument) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("id"),
		field.String("document_key").MaxLen(128).Unique(),
		field.Int("current_version").Default(0),
		field.Time("created_at").
			Default(time.Now).
			Immutable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("updated_at").
			Default(time.Now).
			UpdateDefault(time.Now).
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

// Edges of the RagDocument.
func (RagDocument) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("versions", RagDocumentVersion.Type).
			Annotations(entsql.OnDelete(entsql.Cascade)),
	}
}

// Annotations fixes the entity to the externally managed PostgreSQL table name.
func (RagDocument) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "rag_documents"}}
}
