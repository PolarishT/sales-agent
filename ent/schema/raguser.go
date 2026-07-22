package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
)

// RagUser holds the schema definition for the RagUser entity.
type RagUser struct {
	ent.Schema
}

// Fields of the RagUser.
func (RagUser) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("id"),
		field.String("user_id").MaxLen(128).Unique(),
		field.JSON("metadata", map[string]any{}).
			Default(func() map[string]any { return map[string]any{} }).
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}),
		field.Time("first_seen_at").
			Default(time.Now).
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("last_seen_at").
			Default(time.Now).
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

// Edges of the RagUser.
func (RagUser) Edges() []ent.Edge {
	return nil
}

// Annotations fixes the entity to the existing PostgreSQL table name.
func (RagUser) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "rag_users"}}
}
