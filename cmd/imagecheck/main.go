// imagecheck is an isolated CI helper. It is exported from the build stage and
// mounted read-only into the exact runtime image; it is not shipped in that image.
package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"time"

	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/llm/onnx"
	"github.com/iulita-ai/iulita/internal/storage/sqlite"
	"go.uber.org/zap"
)

func main() {
	if err := check(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("SQLite write/read and offline preloaded embedding passed")
}

func check() (resultErr error) {
	dir, err := os.MkdirTemp("", "iulita-imagecheck-")
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, os.RemoveAll(dir)) }()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	store, err := sqlite.New(filepath.Join(dir, "fixture.db"))
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, store.Close()) }()
	if migrationErr := store.RunMigrations(ctx); migrationErr != nil {
		return migrationErr
	}
	msg := &domain.ChatMessage{ChatID: "imagecheck", UserID: "fixture", Role: domain.RoleUser, Content: "isolated image check"}
	if saveErr := store.SaveMessage(ctx, msg); saveErr != nil {
		return saveErr
	}
	history, err := store.GetHistory(ctx, msg.ChatID, 10)
	if err != nil {
		return fmt.Errorf("SQLite round trip failed: %w", err)
	}
	if len(history) != 1 || history[0].Content != msg.Content {
		return fmt.Errorf("SQLite round trip lost the fixture")
	}
	provider, err := onnx.New(filepath.Join(dir, "empty-model-cache"), "", zap.NewNop())
	if err != nil {
		return err
	}
	defer provider.Close()
	vectors, err := provider.Embed(ctx, []string{"isolated image check"})
	if err != nil {
		return fmt.Errorf("embedding shape failed: %w", err)
	}
	if len(vectors) != 1 || len(vectors[0]) != provider.Dimensions() {
		return fmt.Errorf("embedding shape changed")
	}
	var norm float64
	for _, v := range vectors[0] {
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
			return fmt.Errorf("embedding contains non-finite values")
		}
		norm += float64(v) * float64(v)
	}
	if norm == 0 {
		return fmt.Errorf("embedding is empty")
	}
	return nil
}
