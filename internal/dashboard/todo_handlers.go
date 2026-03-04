package dashboard

import (
	"fmt"
	"strconv"
	"time"

	"github.com/gofiber/fiber/v2"
	"go.uber.org/zap"

	"github.com/iulita-ai/iulita/internal/auth"
	"github.com/iulita-ai/iulita/internal/domain"
)

// handleTodoProviders returns available todo providers.
func (s *Server) handleTodoProviders(c *fiber.Ctx) error {
	defaultProvider := ""
	if s.configStore != nil {
		if v, ok := s.configStore.Get("skills.tasks.default_provider"); ok && v != "" {
			defaultProvider = v
		}
	}

	infos := []TodoProviderInfo{
		{
			ID:        "builtin",
			Name:      "Iulita.ai",
			Available: true,
			IsDefault: defaultProvider == "" || defaultProvider == "builtin",
		},
	}

	for _, p := range s.todoProviders {
		infos = append(infos, TodoProviderInfo{
			ID:        p.ProviderID(),
			Name:      p.ProviderName(),
			Available: p.IsAvailable(),
			IsDefault: defaultProvider == p.ProviderID(),
		})
	}

	return c.JSON(fiber.Map{
		"providers":        infos,
		"default_provider": defaultProvider,
	})
}

// handleTodosToday returns tasks due today.
func (s *Server) handleTodosToday(c *fiber.Ctx) error {
	userID, err := s.requireUserID(c)
	if err != nil {
		return err
	}

	now := s.userNow(c)
	items, err := s.store.ListTodoItemsDueToday(c.Context(), userID, now)
	if err != nil {
		return s.errorResponse(c, err)
	}

	return c.JSON(fiber.Map{
		"items": items,
		"count": len(items),
	})
}

// handleTodosOverdue returns overdue tasks.
func (s *Server) handleTodosOverdue(c *fiber.Ctx) error {
	userID, err := s.requireUserID(c)
	if err != nil {
		return err
	}

	now := s.userNow(c)
	items, err := s.store.ListTodoItemsOverdue(c.Context(), userID, now)
	if err != nil {
		return s.errorResponse(c, err)
	}

	return c.JSON(fiber.Map{
		"items": items,
		"count": len(items),
	})
}

// handleTodosUpcoming returns upcoming tasks.
func (s *Server) handleTodosUpcoming(c *fiber.Ctx) error {
	userID, err := s.requireUserID(c)
	if err != nil {
		return err
	}

	days := 7
	if d := c.Query("days"); d != "" {
		if parsed, err := strconv.Atoi(d); err == nil && parsed > 0 && parsed <= 30 {
			days = parsed
		}
	}

	now := s.userNow(c)
	items, err := s.store.ListTodoItemsUpcoming(c.Context(), userID, now, days)
	if err != nil {
		return s.errorResponse(c, err)
	}

	return c.JSON(fiber.Map{
		"items": items,
		"count": len(items),
	})
}

// handleTodosAll returns all incomplete tasks.
func (s *Server) handleTodosAll(c *fiber.Ctx) error {
	userID, err := s.requireUserID(c)
	if err != nil {
		return err
	}

	limit := 100
	if l := c.Query("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 {
			limit = parsed
		}
	}

	items, err := s.store.ListTodoItemsAll(c.Context(), userID, limit)
	if err != nil {
		return s.errorResponse(c, err)
	}

	return c.JSON(fiber.Map{
		"items": items,
		"count": len(items),
	})
}

// handleCreateTodo creates a new task.
func (s *Server) handleCreateTodo(c *fiber.Ctx) error {
	userID, err := s.requireUserID(c)
	if err != nil {
		return err
	}

	var req CreateTodoRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid request body"})
	}
	if req.Title == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "title is required"})
	}

	provider := req.Provider
	if provider == "" {
		provider = "builtin"
		if s.configStore != nil {
			if v, ok := s.configStore.Get("skills.tasks.default_provider"); ok && v != "" {
				provider = v
			}
		}
	}

	// For external providers, create via API then sync locally.
	if provider != "builtin" {
		for _, p := range s.todoProviders {
			if p.ProviderID() == provider && p.IsAvailable() {
				item, err := p.CreateTask(c.Context(), userID, req)
				if err != nil {
					return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{
						"error": fmt.Sprintf("failed to create task in %s: %v", provider, err),
					})
				}
				// Save locally too.
				item.UserID = userID
				if err := s.store.UpsertTodoItemByExternal(c.Context(), item); err != nil {
					s.logger.Error("failed to save synced todo locally", zap.Error(err))
				}
				return c.Status(fiber.StatusCreated).JSON(item)
			}
		}
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": fmt.Sprintf("provider %q not available", provider),
		})
	}

	// Built-in: create directly in DB.
	item := &domain.TodoItem{
		UserID:   userID,
		Provider: "builtin",
		Title:    req.Title,
		Notes:    req.Notes,
		Priority: req.Priority,
	}
	if req.DueDate != "" {
		if d, err := time.Parse("2006-01-02", req.DueDate); err == nil {
			item.DueDate = &d
		}
	}

	if err := s.store.CreateTodoItem(c.Context(), item); err != nil {
		return s.errorResponse(c, err)
	}

	return c.Status(fiber.StatusCreated).JSON(item)
}

// handleCompleteTodo marks a task as complete.
func (s *Server) handleCompleteTodo(c *fiber.Ctx) error {
	userID, err := s.requireUserID(c)
	if err != nil {
		return err
	}

	id, err := strconv.ParseInt(c.Params("id"), 10, 64)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid task ID"})
	}

	// Get the item to check provider.
	item, err := s.store.GetTodoItem(c.Context(), id, userID)
	if err != nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "task not found"})
	}

	// Complete in external system if applicable.
	if item.Provider != "builtin" && item.ExternalID != "" {
		for _, p := range s.todoProviders {
			if p.ProviderID() == item.Provider && p.IsAvailable() {
				if err := p.CompleteTask(c.Context(), item.ExternalID); err != nil {
					return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{
						"error": fmt.Sprintf("failed to complete in %s: %v", item.Provider, err),
					})
				}
				break
			}
		}
	}

	// Complete locally.
	if err := s.store.CompleteTodoItem(c.Context(), id, userID); err != nil {
		return s.errorResponse(c, err)
	}

	return c.JSON(fiber.Map{"status": "completed", "id": id})
}

// handleDeleteTodo deletes a builtin task.
func (s *Server) handleDeleteTodo(c *fiber.Ctx) error {
	userID, err := s.requireUserID(c)
	if err != nil {
		return err
	}

	id, err := strconv.ParseInt(c.Params("id"), 10, 64)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid task ID"})
	}

	// Check it's a builtin task.
	item, err := s.store.GetTodoItem(c.Context(), id, userID)
	if err != nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "task not found"})
	}
	if item.Provider != "builtin" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "can only delete builtin tasks; complete external tasks instead",
		})
	}

	if err := s.store.DeleteTodoItem(c.Context(), id, userID); err != nil {
		return s.errorResponse(c, err)
	}

	return c.JSON(fiber.Map{"status": "deleted", "id": id})
}

// handleTodoSync triggers a manual sync.
func (s *Server) handleTodoSync(c *fiber.Ctx) error {
	if s.taskScheduler == nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "scheduler not available"})
	}

	if err := s.taskScheduler.TriggerJob(c.Context(), "todo_sync"); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}

	return c.Status(fiber.StatusAccepted).JSON(fiber.Map{"status": "sync_triggered"})
}

// handleSetDefaultProvider sets the default task provider.
func (s *Server) handleSetDefaultProvider(c *fiber.Ctx) error {
	if s.configStore == nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "config store not available"})
	}

	var body struct {
		Provider string `json:"provider"`
	}
	if err := c.BodyParser(&body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid request body"})
	}

	updatedBy := ""
	if claims := auth.GetClaims(c); claims != nil {
		updatedBy = claims.Username
	}
	if err := s.configStore.Set(c.Context(), "skills.tasks.default_provider", body.Provider, updatedBy, false); err != nil {
		return s.errorResponse(c, err)
	}

	return c.JSON(fiber.Map{"status": "ok", "default_provider": body.Provider})
}

// handleTodoCounts returns count summaries.
func (s *Server) handleTodoCounts(c *fiber.Ctx) error {
	userID, err := s.requireUserID(c)
	if err != nil {
		return err
	}

	now := s.userNow(c)

	today, err := s.store.ListTodoItemsDueToday(c.Context(), userID, now)
	if err != nil {
		return s.errorResponse(c, err)
	}

	overdue, err := s.store.ListTodoItemsOverdue(c.Context(), userID, now)
	if err != nil {
		return s.errorResponse(c, err)
	}

	return c.JSON(fiber.Map{
		"today":   len(today),
		"overdue": len(overdue),
	})
}

// --- helpers ---

func (s *Server) requireUserID(c *fiber.Ctx) (string, error) {
	claims := auth.GetClaims(c)
	if claims == nil {
		return "", c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
	}
	if claims.UserID == "" {
		return "", c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "no user ID in token"})
	}
	return claims.UserID, nil
}

// userNow returns the current time in the user's timezone (from JWT or profile).
func (s *Server) userNow(c *fiber.Ctx) time.Time {
	claims := auth.GetClaims(c)
	if claims != nil && claims.UserID != "" {
		if user, err := s.store.GetUser(c.Context(), claims.UserID); err == nil && user.Timezone != "" {
			if loc, err := time.LoadLocation(user.Timezone); err == nil {
				return time.Now().In(loc)
			}
		}
	}
	return time.Now()
}
