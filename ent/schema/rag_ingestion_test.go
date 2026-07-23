package schema

import (
	"testing"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	entschema "entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
)

func TestRagChunkEmbeddingUsesVector1024(t *testing.T) {
	fields := fieldMap((RagChunk{}).Fields())
	embedding := fields["embedding"]
	if embedding.Info.Type != field.TypeOther {
		t.Fatalf("embedding type = %v", embedding.Info.Type)
	}
	if got := embedding.SchemaType[dialect.Postgres]; got != "vector(1024)" {
		t.Fatalf("embedding postgres type = %q", got)
	}
}

func TestRagIngestionTableNames(t *testing.T) {
	assertTable(t, (RagDocument{}).Annotations(), "rag_documents")
	assertTable(t, (RagDocumentVersion{}).Annotations(), "rag_document_versions")
	assertTable(t, (RagChunk{}).Annotations(), "rag_chunks")
}

func fieldMap(fields []ent.Field) map[string]*field.Descriptor {
	result := make(map[string]*field.Descriptor, len(fields))
	for _, configured := range fields {
		descriptor := configured.Descriptor()
		if descriptor.Err != nil {
			panic(descriptor.Err)
		}
		result[descriptor.Name] = descriptor
	}
	return result
}

func assertTable(t *testing.T, annotations []entschema.Annotation, want string) {
	t.Helper()
	if len(annotations) != 1 {
		t.Fatalf("annotation count = %d", len(annotations))
	}
	annotation, ok := annotations[0].(entsql.Annotation)
	if !ok || annotation.Table != want {
		t.Fatalf("table annotation = %#v, want %s", annotations[0], want)
	}
}

func TestRagIngestionFieldShapes(t *testing.T) {
	documents := fieldMap((RagDocument{}).Fields())
	if key := documents["document_key"]; key.Size != 128 || !key.Unique || key.Optional {
		t.Fatalf("document_key = %#v", key)
	}
	if current := documents["current_version"]; current.Info.Type != field.TypeInt || current.Default == nil {
		t.Fatalf("current_version = %#v", current)
	}

	versions := fieldMap((RagDocumentVersion{}).Fields())
	if versions["ingestion_id"].Info.Type != field.TypeUUID || !versions["ingestion_id"].Unique {
		t.Fatalf("ingestion_id = %#v", versions["ingestion_id"])
	}
	for _, name := range []string{"failure_code", "failure_message", "completed_at"} {
		if !versions[name].Optional || !versions[name].Nillable {
			t.Fatalf("%s = %#v", name, versions[name])
		}
	}

	chunks := fieldMap((RagChunk{}).Fields())
	if chunks["heading_path"].SchemaType[dialect.Postgres] != "jsonb" || chunks["heading_path"].Default == nil {
		t.Fatalf("heading_path = %#v", chunks["heading_path"])
	}
	if chunks["document_version_id"].Optional {
		t.Fatalf("document_version_id = %#v", chunks["document_version_id"])
	}
}
