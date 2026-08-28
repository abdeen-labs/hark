package db

import (
	"strings"
	"testing"
	"testing/fstest"
)

func TestLoadMigrationsOrdersByVersion(t *testing.T) {
	fsys := fstest.MapFS{
		"0010_add_devices.sql":    {Data: []byte("CREATE TABLE device ();")},
		"0002_add_service.sql":    {Data: []byte("CREATE TABLE service ();")},
		"0001_initial_schema.sql": {Data: []byte(`CREATE TABLE "user" ();`)},
		".keep":                   {Data: []byte("notes")},
		"README.md":               {Data: []byte("# migrations")},
	}

	got, err := LoadMigrations(fsys)
	if err != nil {
		t.Fatalf("LoadMigrations: %v", err)
	}
	want := []int64{1, 2, 10}
	if len(got) != len(want) {
		t.Fatalf("loaded %d migrations, want %d: %v", len(got), len(want), got)
	}
	for i, v := range want {
		if got[i].Version != v {
			t.Errorf("migration %d has version %d, want %d", i, got[i].Version, v)
		}
	}
	if got[0].Name != "initial_schema" {
		t.Errorf("Name = %q, want %q", got[0].Name, "initial_schema")
	}
	if got[0].String() != "0001_initial_schema" {
		t.Errorf("String() = %q", got[0].String())
	}
	if len(got[0].Checksum) != 64 {
		t.Errorf("Checksum = %q, want 64 hex chars", got[0].Checksum)
	}
}

func TestLoadMigrationsChecksumTracksContent(t *testing.T) {
	before, err := LoadMigrations(fstest.MapFS{"0001_a.sql": {Data: []byte("SELECT 1;")}})
	if err != nil {
		t.Fatal(err)
	}
	after, err := LoadMigrations(fstest.MapFS{"0001_a.sql": {Data: []byte("SELECT 2;")}})
	if err != nil {
		t.Fatal(err)
	}
	if before[0].Checksum == after[0].Checksum {
		t.Error("checksum did not change with content")
	}
}

func TestLoadMigrationsRejectsBadInput(t *testing.T) {
	cases := map[string]struct {
		fsys fstest.MapFS
		want string
	}{
		"bad name": {
			fsys: fstest.MapFS{"create-user.sql": {Data: []byte("SELECT 1;")}},
			want: "must be named",
		},
		"uppercase name": {
			fsys: fstest.MapFS{"0001_CreateUser.sql": {Data: []byte("SELECT 1;")}},
			want: "must be named",
		},
		"duplicate version": {
			fsys: fstest.MapFS{
				"0001_a.sql": {Data: []byte("SELECT 1;")},
				"1_b.sql":    {Data: []byte("SELECT 2;")},
			},
			want: "share version",
		},
		"zero version": {
			fsys: fstest.MapFS{"0000_a.sql": {Data: []byte("SELECT 1;")}},
			want: "invalid version",
		},
		"empty file": {
			fsys: fstest.MapFS{"0001_a.sql": {Data: []byte("   \n")}},
			want: "is empty",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := LoadMigrations(tc.fsys)
			if err == nil {
				t.Fatal("LoadMigrations succeeded, want error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

// TestEmbeddedMigrationsLoad guards the embedded set: every file that ships in
// the binary must be a well-formed migration.
func TestEmbeddedMigrationsLoad(t *testing.T) {
	migrations, err := LoadMigrations(Migrations())
	if err != nil {
		t.Fatalf("embedded migrations are invalid: %v", err)
	}
	if len(migrations) != 1 || migrations[0].Version != 1 {
		t.Fatalf("embedded schema = %v, want one clean initial schema", migrations)
	}
	for i, m := range migrations {
		if i > 0 && m.Version <= migrations[i-1].Version {
			t.Fatalf("migration %s is not ordered after %s", m, migrations[i-1])
		}
	}
	t.Logf("embedded migrations: %d", len(migrations))
}

func TestInitialMigrationChecksum(t *testing.T) {
	migrations, err := LoadMigrations(Migrations())
	if err != nil {
		t.Fatalf("load embedded migrations: %v", err)
	}

	const checksum = "8964572d41e23c83fa8f1314e42a5bd2f933200b39c78c01bb370a75d09d88df"
	for _, migration := range migrations {
		if migration.Version == 1 {
			if migration.Checksum != checksum {
				t.Fatalf("migration %s changed; update the checksum only when intentionally resetting the database", migration)
			}
			return
		}
	}
	t.Fatal("initial migration is missing")
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
