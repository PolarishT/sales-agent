package schema

import (
	"testing"

	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema/field"
)

func TestRagUserTableName(t *testing.T) {
	annotations := (RagUser{}).Annotations()
	if len(annotations) != 1 {
		t.Fatalf("len(Annotations()) = %d, want 1", len(annotations))
	}
	annotation, ok := annotations[0].(entsql.Annotation)
	if !ok || annotation.Table != "rag_users" {
		t.Fatalf("table annotation = %#v, want rag_users", annotations[0])
	}
}

func TestRagUserFieldsMatchExistingTable(t *testing.T) {
	fields := make(map[string]*field.Descriptor)
	for _, configuredField := range (RagUser{}).Fields() {
		descriptor := configuredField.Descriptor()
		if descriptor.Err != nil {
			t.Fatalf("field %q: %v", descriptor.Name, descriptor.Err)
		}
		fields[descriptor.Name] = descriptor
	}
	if len(fields) != 5 {
		t.Fatalf("field count = %d, want 5", len(fields))
	}
	if fields["id"].Info.Type != field.TypeInt64 {
		t.Fatalf("id type = %v, want int64", fields["id"].Info.Type)
	}
	if userID := fields["user_id"]; userID.Info.Type != field.TypeString || userID.Size != 128 || !userID.Unique || userID.Optional {
		t.Fatalf("user_id descriptor = %#v", userID)
	}
	metadata := fields["metadata"]
	if metadata.Info.Type != field.TypeJSON || metadata.SchemaType[dialect.Postgres] != "jsonb" || metadata.Default == nil || metadata.Optional {
		t.Fatalf("metadata descriptor = %#v", metadata)
	}
	for _, name := range []string{"first_seen_at", "last_seen_at"} {
		timestamp := fields[name]
		if timestamp.Info.Type != field.TypeTime || timestamp.SchemaType[dialect.Postgres] != "timestamptz" || timestamp.Default == nil || timestamp.Optional {
			t.Fatalf("%s descriptor = %#v", name, timestamp)
		}
		if timestamp.Immutable || timestamp.UpdateDefault != nil {
			t.Fatalf("%s adds update semantics not present in the database", name)
		}
	}
}
