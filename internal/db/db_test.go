package db_test

import (
	"context"
	"sync"
	"testing"

	"whiteboard/internal/db"
	"whiteboard/internal/db/dbtest"
)

func TestMigrateIsIdempotent(t *testing.T) {
	pool := dbtest.NewPool(t)
	ctx := context.Background()

	applied, err := db.Migrate(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) == 0 {
		t.Fatal("expected migrations to be applied on an empty database")
	}
	again, err := db.Migrate(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 0 {
		t.Fatalf("second run applied %v, want nothing", again)
	}

	if _, err := pool.Exec(ctx, "INSERT INTO boards (id) VALUES ('demo')"); err != nil {
		t.Fatalf("boards table unusable after migrate: %v", err)
	}
}

func TestConcurrentMigrateAppliesOnce(t *testing.T) {
	pool := dbtest.NewPool(t)
	ctx := context.Background()

	const nodes = 5
	var wg sync.WaitGroup
	results := make([][]string, nodes)
	errs := make([]error, nodes)
	for i := range nodes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = db.Migrate(ctx, pool)
		}()
	}
	wg.Wait()

	total := 0
	for i := range nodes {
		if errs[i] != nil {
			t.Fatalf("node %d: %v", i, errs[i])
		}
		total += len(results[i])
	}
	var recorded int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM schema_migrations").Scan(&recorded); err != nil {
		t.Fatal(err)
	}
	if total != recorded {
		t.Fatalf("nodes applied %d migrations in total, schema_migrations has %d", total, recorded)
	}
}
