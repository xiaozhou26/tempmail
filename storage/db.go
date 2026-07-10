package storage

import (
	"fmt"
	"os"
	"path/filepath"

	"tempmail/models"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Open initialises the SQLite database at dbPath, creating the parent
// directory if needed, auto-migrating the schema, and returning a *gorm.DB.
func Open(dbPath string) (*gorm.DB, error) {
	if dir := filepath.Dir(dbPath); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create db dir: %w", err)
		}
	}

	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Warn),
	})
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	if err := db.AutoMigrate(&models.Mailbox{}, &models.Message{}); err != nil {
		return nil, fmt.Errorf("auto-migrate: %w", err)
	}
	return db, nil
}
