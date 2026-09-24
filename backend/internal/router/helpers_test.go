package router

import (
	"io"
	"log/slog"
	"strconv"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/mockhub/mockhub/internal/model"
)

// newIntegrationDB opens an isolated in-memory SQLite database with the same
// schema AutoMigrate creates in production, so router tests exercise the real
// persistence path.
func newIntegrationDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(
		&model.User{},
		&model.Project{},
		&model.MockAPI{},
		&model.ResponseTemplate{},
		&model.RequestLog{},
	); err != nil {
		t.Fatalf("auto migrate: %v", err)
	}
	return db
}

func discardSlog(_ *testing.T) *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func itoa(id uint) string {
	return strconv.FormatUint(uint64(id), 10)
}
