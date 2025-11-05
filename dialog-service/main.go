package main

import (
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// Models
type DialogMessage struct {
	From      string    `json:"from"`
	To        string    `json:"to"`
	Text      string    `json:"text"`
	Timestamp time.Time `json:"timestamp"`
}

type MessageSendRequest struct {
	Text string `json:"text" binding:"required"`
}

// Storage for dialog service
type DialogStorage struct {
	dialogs map[string][]*DialogMessage // sorted userIds key -> messages
	mu      sync.RWMutex
}

var storage = &DialogStorage{
	dialogs: make(map[string][]*DialogMessage),
}

// Helper functions
func createDialogKey(userId1, userId2 string) string {
	if userId1 < userId2 {
		return userId1 + "_" + userId2
	}
	return userId2 + "_" + userId1
}

// Middleware для получения userId из заголовка
func userContextMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		userId := c.GetHeader("X-User-ID")
		if userId == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"message": "User ID header required"})
			c.Abort()
			return
		}

		c.Set("userId", userId)
		c.Next()
	}
}

// Dialog handlers
func sendMessage(c *gin.Context) {
	currentUserId := c.GetString("userId")
	toUserId := c.Param("user_id")
	
	var req MessageSendRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "Invalid request data"})
		return
	}

	if currentUserId == toUserId {
		c.JSON(http.StatusBadRequest, gin.H{"message": "Cannot send message to yourself"})
		return
	}

	message := &DialogMessage{
		From:      currentUserId,
		To:        toUserId,
		Text:      req.Text,
		Timestamp: time.Now(),
	}

	dialogKey := createDialogKey(currentUserId, toUserId)
	
	storage.mu.Lock()
	storage.dialogs[dialogKey] = append(storage.dialogs[dialogKey], message)
	storage.mu.Unlock()

	log.Printf("Message sent from %s to %s: %s", currentUserId, toUserId, req.Text)
	c.JSON(http.StatusOK, gin.H{"message": "Message sent successfully"})
}

func getDialog(c *gin.Context) {
	currentUserId := c.GetString("userId")
	otherUserId := c.Param("user_id")

	dialogKey := createDialogKey(currentUserId, otherUserId)
	
	storage.mu.RLock()
	messages := storage.dialogs[dialogKey]
	storage.mu.RUnlock()

	if messages == nil {
		messages = []*DialogMessage{}
	}

	log.Printf("Retrieved %d messages for dialog between %s and %s", len(messages), currentUserId, otherUserId)
	c.JSON(http.StatusOK, messages)
}

// Health check для мониторинга
func healthCheck(c *gin.Context) {
	storage.mu.RLock()
	totalMessages := 0
	totalDialogs := len(storage.dialogs)
	for _, messages := range storage.dialogs {
		totalMessages += len(messages)
	}
	storage.mu.RUnlock()

	c.JSON(http.StatusOK, gin.H{
		"status": "ok",
		"service": "dialog-service",
		"stats": gin.H{
			"total_dialogs": totalDialogs,
			"total_messages": totalMessages,
		},
	})
}

// Получение всех диалогов пользователя
func getUserDialogs(c *gin.Context) {
	currentUserId := c.GetString("userId")
	
	storage.mu.RLock()
	userDialogs := make(map[string][]*DialogMessage)
	
	for dialogKey, messages := range storage.dialogs {
		// Проверяем, участвует ли текущий пользователь в диалоге
		if len(messages) > 0 {
			firstMessage := messages[0]
			if firstMessage.From == currentUserId || firstMessage.To == currentUserId {
				userDialogs[dialogKey] = messages
			}
		}
	}
	storage.mu.RUnlock()

	c.JSON(http.StatusOK, userDialogs)
}

func setupRoutes() *gin.Engine {
	r := gin.Default()

	// CORS middleware
	r.Use(func(c *gin.Context) {
		c.Writer.Header().Set("Access-Control-Allow-Origin", "*")
		c.Writer.Header().Set("Access-Control-Allow-Credentials", "true")
		c.Writer.Header().Set("Access-Control-Allow-Headers", "Content-Type, Content-Length, Accept-Encoding, X-CSRF-Token, Authorization, accept, origin, Cache-Control, X-Requested-With, X-User-ID")
		c.Writer.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS, GET, PUT, DELETE")

		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}

		c.Next()
	})

	// Health check
	r.GET("/health", healthCheck)

	// Protected routes (требуют X-User-ID заголовок)
	protected := r.Group("/")
	protected.Use(userContextMiddleware())
	{
		// Dialog routes
		protected.POST("/dialog/:user_id/send", sendMessage)
		protected.GET("/dialog/:user_id/list", getDialog)
		
		protected.GET("/dialogs", getUserDialogs)
	}

	return r
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8081"
	}

	r := setupRoutes()
	
	log.Printf("Dialog Service starting on port %s", port)
	log.Fatal(r.Run(":" + port))
}