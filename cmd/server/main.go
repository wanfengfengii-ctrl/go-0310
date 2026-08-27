// Command server is the executable entry point for the thermal-vacuum test
// gate HTTP service. It opens the embedded database, applies the schema,
// restores state (expiring stale leases), and serves the JSON API.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"time"

	"thermal-vacuum-test-gate/internal/httpapi"
	"thermal-vacuum-test-gate/internal/recovery"
	"thermal-vacuum-test-gate/internal/store/sqlite"
)

func main() {
	addr := os.Getenv("TVTG_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	dsn := os.Getenv("TVTG_DB")
	if dsn == "" {
		dsn = "file:thermal-vacuum.db"
	}

	db, err := sqlite.Open(dsn)
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	if err := db.Migrate(ctx); err != nil {
		log.Fatalf("migrate: %v", err)
	}

	now := func() int64 { return time.Now().UnixMilli() }
	rep, err := recovery.Recover(ctx, db, now)
	if err != nil {
		log.Fatalf("recover: %v", err)
	}
	log.Printf("recovery complete: runs=%d leases_expired=%d", rep.RunsRecovered, rep.LeasesExpired)
	if rep.IntegrityError != "" {
		log.Printf("recovery warning: %s", rep.IntegrityError)
	}

	srv := httpapi.NewServer(db, now)
	log.Printf("thermal-vacuum-test-gate listening on %s", addr)
	if err := http.ListenAndServe(addr, srv.Handler()); err != nil {
		log.Fatalf("server: %v", err)
	}
}
