package storage

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"tempmail/models"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Open initialises the SQLite database at dbPath, creating the parent
// directory if needed, auto-migrating the schema, and returning a *gorm.DB.
//
// The connection is tuned for concurrent reads (many clients polling different
// mailboxes at once): WAL journalling lets readers run without blocking the
// writer, and a pooled set of connections serves those reads in parallel.
func Open(dbPath string) (*gorm.DB, error) {
	if dir := filepath.Dir(dbPath); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create db dir: %w", err)
		}
	}

	db, err := gorm.Open(sqlite.Open(buildDSN(dbPath)), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Warn),
		// Repeated list/get queries are prepared once and reused across requests.
		PrepareStmt: true,
		// Inbound stores are single statements; skip the implicit per-call
		// transaction wrapper to cut write overhead under load.
		SkipDefaultTransaction: true,
	})
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("get sql.DB: %w", err)
	}
	// WAL supports many concurrent readers; writes still serialize inside SQLite
	// where busy_timeout absorbs the contention. A modest pool keeps read
	// throughput high without exhausting file handles.
	sqlDB.SetMaxOpenConns(16)
	sqlDB.SetMaxIdleConns(16)
	sqlDB.SetConnMaxIdleTime(5 * time.Minute)

	if err := db.AutoMigrate(&models.Mailbox{}, &models.Message{}); err != nil {
		return nil, fmt.Errorf("auto-migrate: %w", err)
	}
	return db, nil
}

// buildDSN appends the per-connection PRAGMAs that make SQLite behave well
// under concurrent reads. The driver re-applies these query params to every
// pooled connection, not just the first:
//
//	journal_mode=WAL   readers never block the writer and vice versa
//	busy_timeout=5000  wait up to 5s for a lock instead of failing immediately
//	synchronous=NORMAL safe under WAL, far fewer fsyncs than FULL
//	foreign_keys=ON    enforce the mailbox→message cascade delete
func buildDSN(dbPath string) string {
	pragmas := url.Values{}
	pragmas.Add("_pragma", "journal_mode(WAL)")
	pragmas.Add("_pragma", "busy_timeout(5000)")
	pragmas.Add("_pragma", "synchronous(NORMAL)")
	pragmas.Add("_pragma", "foreign_keys(ON)")

	sep := "?"
	if strings.Contains(dbPath, "?") {
		sep = "&"
	}
	return dbPath + sep + pragmas.Encode()
}
