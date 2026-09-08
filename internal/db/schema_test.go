package db

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
)

func TestEmbeddedSchemaDefinesFinalTables(t *testing.T) {
	files, err := fs.Sub(schemaFS, "schema")
	if err != nil {
		t.Fatal(err)
	}
	definition, _, err := readSchema(files)
	if err != nil {
		t.Fatal(err)
	}
	withoutComments := regexp.MustCompile(`(?m)--.*$`).ReplaceAllString(definition, "")
	for _, statement := range strings.Split(withoutComments, ";") {
		statement = strings.TrimSpace(statement)
		if statement != "" && !regexp.MustCompile(`(?i)^CREATE (TABLE|(?:UNIQUE )?INDEX)\b`).MatchString(statement) {
			t.Fatalf("schema contains a non-create statement: %s", statement)
		}
	}
}

func TestReadSchemaRejectsEmptyDefinition(t *testing.T) {
	for _, files := range []fstest.MapFS{{}, {"001_empty.sql": {Data: []byte(" \n")}}} {
		if _, _, err := readSchema(files); err == nil {
			t.Fatal("empty schema accepted")
		}
	}
}

func TestRedact(t *testing.T) {
	for in, want := range map[string]string{
		"postgres://hark:secret@db:5432/hark?sslmode=disable": "postgres://hark:xxxxx@db:5432/hark",
		"postgres://db:5432/hark":                             "postgres://db:5432/hark",
		"://nope":                                             "<invalid url>",
	} {
		if got := Redact(in); got != want {
			t.Errorf("Redact(%q) = %q, want %q", in, got, want)
		}
	}
}
