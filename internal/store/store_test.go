package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func TestLegacyMigrationAndRecovery(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.sqlite")
	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = old.Exec(`CREATE TABLE users(id INTEGER PRIMARY KEY,name TEXT,code TEXT,partner INTEGER,ai INTEGER);
 CREATE TABLE requests(sender INTEGER,target INTEGER,created REAL);
 CREATE TABLE places(id INTEGER PRIMARY KEY,owner INTEGER,partner INTEGER,title TEXT,category TEXT,note TEXT);
 CREATE TABLE likes(place INTEGER,user INTEGER);
 CREATE TABLE cooldowns(user INTEGER,action TEXT,at REAL);
 INSERT INTO users VALUES(1,'Alice','AAA',2,1),(2,'Bob','BBB',1,1),(3,'Solo','CCC',NULL,0);
 INSERT INTO places VALUES(5,1,2,'Cafe','Food','Original note'),(6,3,1,'Park','Walk','Solo idea');
 INSERT INTO likes VALUES(5,1),(5,2),(5,3);`)
	old.Close()
	if err != nil {
		t.Fatal(err)
	}
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	var n int
	for query, want := range map[string]int{"SELECT count(*) FROM pairs": 1, "SELECT count(*) FROM places": 2, "SELECT count(*) FROM place_preferences": 2, "SELECT count(*) FROM consents": 0, "SELECT count(*) FROM places WHERE personal_user_id=3": 1} {
		if err = db.DB.QueryRowContext(ctx, query).Scan(&n); err != nil || n != want {
			t.Fatalf("%s got %d want %d err %v", query, n, want, err)
		}
	}
	rows, err := db.DB.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		t.Fatal(err)
	}
	bad := rows.Next()
	rows.Close()
	if bad {
		t.Fatal("broken foreign keys")
	}
	db.DB.Close()
	snapshot := path + ".pre-v1.sqlite"
	if _, err = os.Stat(snapshot); err != nil {
		t.Fatal(err)
	}
	recovery, err := sql.Open("sqlite", snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err = recovery.QueryRow("SELECT count(*) FROM users WHERE ai=1").Scan(&n); err != nil || n != 2 {
		t.Fatal("invalid recovery", err)
	}
	recovery.Close()
	db, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	if err = db.DB.QueryRowContext(ctx, "SELECT count(*) FROM places").Scan(&n); err != nil || n != 2 {
		t.Fatal("migration repeated", err)
	}
}

func TestUnknownSchemaIsNotOverwritten(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "unknown.sqlite")
	raw, _ := sql.Open("sqlite", path)
	raw.Exec("CREATE TABLE valuable(data TEXT)")
	raw.Close()
	if db, err := Open(ctx, path); err == nil {
		db.DB.Close()
		t.Fatal("unknown schema accepted")
	}
}

func TestVersionOneCandidatesMigrateOnceToDraft(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v1.sqlite")
	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = old.Exec(schema); err != nil {
		t.Fatal(err)
	}
	if _, err = old.Exec(`INSERT INTO users(id,name,code,status,created_at) VALUES(1,'A','A','ready',1),(2,'B','B','ready',1);
 INSERT INTO pairs(id,timezone,created_at) VALUES(1,'Europe/Moscow',1);
 INSERT INTO pair_members(pair_id,user_id) VALUES(1,1),(1,2);
 INSERT INTO places(id,pair_id,author_id,title,created_at) VALUES(10,1,1,'Park',1);
 INSERT INTO candidates(pair_id,place_id,added_by,position) VALUES(1,10,1,0);
 PRAGMA user_version=1;`); err != nil {
		t.Fatal(err)
	}
	old.Close()
	for i := 0; i < 2; i++ {
		db, err := Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		var n int
		if err = db.DB.QueryRowContext(ctx, "SELECT count(*) FROM plans WHERE is_draft=1").Scan(&n); err != nil || n != 1 {
			t.Fatal("migration lost/duplicated draft", err, n)
		}
		if err = db.DB.QueryRowContext(ctx, "SELECT count(*) FROM plan_items WHERE place_id=10").Scan(&n); err != nil || n != 1 {
			t.Fatal("place lost", err, n)
		}
		if err = db.DB.QueryRowContext(ctx, "PRAGMA foreign_key_check").Scan(new(any), new(any), new(any), new(any)); err != sql.ErrNoRows {
			t.Fatal("foreign key error", err)
		}
		db.DB.Close()
	}
}
