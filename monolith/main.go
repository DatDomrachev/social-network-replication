package main

import (
  "bufio"
  "bytes"
  "context"
  "encoding/csv"
  "fmt"
  "io"
  "log"
  "math/rand"
  "net/http"
  "os"
  "strconv"
  "strings"
  "sync"
  "time"

  "github.com/gin-gonic/gin"
  "github.com/google/uuid"
  "github.com/jackc/pgx/v5"
  "github.com/jackc/pgx/v5/pgxpool"
  "golang.org/x/crypto/bcrypt"
)

var dialogServiceURL = os.Getenv("DIALOG_SERVICE_URL")

func init() {
  if dialogServiceURL == "" {
    dialogServiceURL = "http://dialog-service:8081"
  }
  log.Printf("Dialog service URL: %s", dialogServiceURL)
}

type User struct {
	ID         string `json:"id"`
	FirstName  string `json:"first_name"`
	SecondName string `json:"second_name"`
	Birthdate  string `json:"birthdate"`
	Biography  string `json:"biography"`
	City       string `json:"city"`
	Password   string `json:"-"`
}

type Post struct {
	ID           string `json:"id"`
	Text         string `json:"text"`
	AuthorUserID string `json:"author_user_id"`
}

type DialogMessage struct {
	From      string    `json:"from"`
	To        string    `json:"to"`
	Text      string    `json:"text"`
	Timestamp time.Time `json:"timestamp"`
}

type LoginRequest struct {
	ID       string `json:"id" binding:"required"`
	Password string `json:"password" binding:"required"`
}

type RegisterRequest struct {
	FirstName  string `json:"first_name" binding:"required"`
	SecondName string `json:"second_name" binding:"required"`
	Birthdate  string `json:"birthdate"`
	Biography  string `json:"biography"`
	City       string `json:"city"`
	Password   string `json:"password" binding:"required"`
}

type PostCreateRequest struct {
	Text string `json:"text" binding:"required"`
}

type MessageSendRequest struct {
	Text string `json:"text" binding:"required"`
}

type LogInsertRequest struct {
	Data string `json:"data" binding:"required"`
}

// Database connections: Master for writes, Slave for reads
var masterDB *pgxpool.Pool
var slaveDB *pgxpool.Pool

// In-memory for friendships and tokens
type Storage struct {
	friendships map[string]map[string]bool // userId -> set of friend userIds
	tokens      map[string]string           // token -> userId
	mu          sync.RWMutex
}

var storage = &Storage{
	friendships: make(map[string]map[string]bool),
	tokens:      make(map[string]string),
}

// Helper lists from people.v2.csv
var firstNames = []string{"Роберт", "Александр", "Илья", "Даниил", "Лев", "Игорь", "Никита", "Юрий", "Егор", "Всеволод", "Демид", "Лука", "Дмитрий", "Иван", "Георгий", "Ярослав", "Платон"}
var secondNames = []string{"Абрамов"}
var cities = []string{"Воткинск", "Домодедово", "Севастополь", "Ржев", "Когалым", "Дзержинск", "Балашов", "Серпухов", "Ногинск", "Новомосковск", "Обнинск", "Омск", "Лесосибирск", "Хасавюрт", "Красноярск", "Барнаул", "Магадан", "Волжск", "Энгельс", "Искитим"}

func main() {
	var err error
	// Master DB
	masterDB, err = pgxpool.New(context.Background(), os.Getenv("MASTER_DB_URL"))
	if err != nil {
		log.Fatalf("Unable to connect to master database: %v", err)
	}
	defer masterDB.Close()

	// Slave DB
	slaveDB, err = pgxpool.New(context.Background(), os.Getenv("SLAVE_DB_URL"))
	if err != nil {
		log.Fatalf("Unable to connect to slave database: %v", err)
	}
	defer slaveDB.Close()

	// Create tables on master
	_, err = masterDB.Exec(context.Background(), `
		CREATE TABLE IF NOT EXISTS users (
			id UUID PRIMARY KEY,
			first_name TEXT NOT NULL,
			second_name TEXT NOT NULL,
			birthdate DATE,
			biography TEXT,
			city TEXT,
			password TEXT NOT NULL
		);
		CREATE TABLE IF NOT EXISTS posts (
			id UUID PRIMARY KEY,
			text TEXT NOT NULL,
			author_user_id UUID NOT NULL REFERENCES users(id)
		);
		CREATE TABLE IF NOT EXISTS logs (
			id SERIAL PRIMARY KEY,
			data TEXT NOT NULL,
			ts TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		);
	`)
	if err != nil {
		log.Fatalf("Failed to create tables: %v", err)
	}

	if len(os.Args) > 1 && os.Args[1] == "-generate" {
		if err := importAndGenerateUsers(); err != nil {
			log.Fatalf("Failed to generate users: %v", err)
		}
		return
	}

	r := setupRoutes()
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	log.Printf("Monolith starting on port %s", port)
	log.Fatal(r.Run(":" + port))
}

func importAndGenerateUsers() error {
	// Import from CSV
	file, err := os.Open("people.v2.csv")
	if err != nil {
		return err
	}
	defer file.Close()

	reader := csv.NewReader(bufio.NewReader(file))
	reader.Comma = ','
	imported := 0
	batch := pgx.Batch{}

	for {
		row, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if len(row) != 3 {
			continue
		}

		fullName := strings.TrimSpace(row[0])
		birthdateStr := strings.TrimSpace(row[1])
		city := strings.TrimSpace(row[2])

		parts := strings.Split(fullName, " ")
		if len(parts) < 2 {
			continue
		}
		secondName := parts[0]
		firstName := parts[1]

		id := uuid.New()
		biography := randomBiography()
		passwordHash, err := bcrypt.GenerateFromPassword([]byte(randomPassword()), bcrypt.DefaultCost)
		if err != nil {
			return err
		}

		birthdate, err := time.Parse("2006-01-02", birthdateStr)
		if err != nil {
			continue
		}

		batch.Queue(
			`INSERT INTO users (id, first_name, second_name, birthdate, biography, city, password) VALUES ($1, $2, $3, $4, $5, $6, $7) 
			 ON CONFLICT DO NOTHING`,
			id, firstName, secondName, birthdate, biography, city, passwordHash,
		)
		imported++
		if batch.Len() >= 1000 {
			br := masterDB.SendBatch(context.Background(), &batch)
			_, err := br.Exec()
			if err != nil {
				return err
			}
			br.Close()
			batch = pgx.Batch{}
			log.Printf("Imported %d users from CSV", imported)
		}
	}

	if batch.Len() > 0 {
		br := masterDB.SendBatch(context.Background(), &batch)
		_, err := br.Exec()
		if err != nil {
			return err
		}
		br.Close()
	}

	log.Printf("Imported %d users from CSV", imported)

	// Generate remaining
	generateCount := 1000000 - imported
	rand.Seed(time.Now().UnixNano())
	for i := 0; i < generateCount; i++ {
		firstName := randomChoice(firstNames)
		secondName := randomChoice(secondNames)
		if rand.Float64() > 0.5 {
			secondName = fmt.Sprintf("%s%d", secondName, rand.Intn(1000))
		}
		birthdate := randomDate()
		biography := randomBiography()
		city := randomChoice(cities)
		passwordHash, _ := bcrypt.GenerateFromPassword([]byte(randomPassword()), bcrypt.DefaultCost)
		id := uuid.New()

		_, err := masterDB.Exec(context.Background(),
			`INSERT INTO users (id, first_name, second_name, birthdate, biography, city, password) VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			id, firstName, secondName, birthdate, biography, city, passwordHash)
		if err != nil {
			return err
		}
	}
	log.Printf("Generated %d additional users", generateCount)
	return nil
}

func randomChoice(choices []string) string {
	return choices[rand.Intn(len(choices))]
}

func randomDate() time.Time {
	min := time.Date(1900, 1, 1, 0, 0, 0, 0, time.UTC).Unix()
	max := time.Date(2010, 12, 31, 0, 0, 0, 0, time.UTC).Unix()
	delta := max - min
	sec := min + rand.Int63n(delta)
	return time.Unix(sec, 0)
}

func randomBiography() string {
	words := []string{"Это", "биография", "пользователя", "из", "социальной", "сети", "с", "разными", "интересами"}
	var sb strings.Builder
	for i := 0; i < 20; i++ {
		sb.WriteString(randomChoice(words) + " ")
	}
	return sb.String()
}

func randomPassword() string {
	chars := "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	var sb strings.Builder
	for i := 0; i < 12; i++ {
		sb.WriteByte(chars[rand.Intn(len(chars))])
	}
	return sb.String()
}

// Auth middleware
func authMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"message": "Authorization header required"})
			c.Abort()
			return
		}

		tokenParts := strings.Split(authHeader, " ")
		if len(tokenParts) != 2 || tokenParts[0] != "Bearer" {
			c.JSON(http.StatusUnauthorized, gin.H{"message": "Invalid authorization format"})
			c.Abort()
			return
		}

		token := tokenParts[1]
		storage.mu.RLock()
		userId, exists := storage.tokens[token]
		storage.mu.RUnlock()

		if !exists {
			c.JSON(http.StatusUnauthorized, gin.H{"message": "Invalid token"})
			c.Abort()
			return
		}

		c.Set("userId", userId)
		c.Next()
	}
}

// Handlers
func login(c *gin.Context) {
	var req LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "Invalid request data"})
		return
	}

	var hashedPassword string
	err := slaveDB.QueryRow(context.Background(), "SELECT password FROM users WHERE id::text = $1", req.ID).Scan(&hashedPassword)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"message": "User not found"})
		return
	}

	if !checkPasswordHash(req.Password, hashedPassword) {
		c.JSON(http.StatusUnauthorized, gin.H{"message": "Invalid password"})
		return
	}

	token := uuid.New().String()
	storage.mu.Lock()
	storage.tokens[token] = req.ID
	storage.mu.Unlock()

	c.JSON(http.StatusOK, gin.H{"token": token})
}

func register(c *gin.Context) {
	var req RegisterRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "Invalid request data"})
		return
	}

	hashedPassword, err := hashPassword(req.Password)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "Failed to hash password"})
		return
	}

	id := uuid.New()
	birthdate, _ := time.Parse("2006-01-02", req.Birthdate)

	_, err = masterDB.Exec(context.Background(),
		"INSERT INTO users (id, first_name, second_name, birthdate, biography, city, password) VALUES ($1, $2, $3, $4, $5, $6, $7)",
		id, req.FirstName, req.SecondName, birthdate, req.Biography, req.City, hashedPassword)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "Failed to register user"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"user_id": id.String()})
}

func getUser(c *gin.Context) {
	id := c.Param("id")
	row := slaveDB.QueryRow(context.Background(), "SELECT id::text, first_name, second_name, birthdate::text, biography, city FROM users WHERE id::text = $1", id)

	u := &User{}
	err := row.Scan(&u.ID, &u.FirstName, &u.SecondName, &u.Birthdate, &u.Biography, &u.City)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"message": "User not found"})
		return
	}

	c.JSON(http.StatusOK, u)
}

func searchUsers(c *gin.Context) {
	firstName := c.Query("first_name")
	secondName := c.Query("second_name")
	if firstName == "" || secondName == "" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "first_name and second_name required"})
		return
	}

	rows, err := slaveDB.Query(context.Background(),
		`SELECT id::text, first_name, second_name, birthdate::text, biography, city 
		 FROM users 
		 WHERE first_name ILIKE $1 || '%' AND second_name ILIKE $2 || '%' 
		 ORDER BY id`,
		firstName, secondName)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "Database error"})
		return
	}
	defer rows.Close()

	var users []*User
	for rows.Next() {
		u := &User{}
		err := rows.Scan(&u.ID, &u.FirstName, &u.SecondName, &u.Birthdate, &u.Biography, &u.City)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"message": "Scan error"})
			return
		}
		users = append(users, u)
	}

	c.JSON(http.StatusOK, users)
}

func addFriend(c *gin.Context) {
	currentUserId := c.GetString("userId")
	friendId := c.Param("user_id")

	var exists bool
	err := slaveDB.QueryRow(context.Background(), "SELECT EXISTS(SELECT 1 FROM users WHERE id::text = $1)", friendId).Scan(&exists)
	if err != nil || !exists {
		c.JSON(http.StatusBadRequest, gin.H{"message": "User not found"})
		return
	}

	storage.mu.Lock()
	if storage.friendships[currentUserId] == nil {
		storage.friendships[currentUserId] = make(map[string]bool)
	}
	storage.friendships[currentUserId][friendId] = true
	storage.mu.Unlock()

	c.JSON(http.StatusOK, gin.H{"message": "Friend added"})
}

func createPost(c *gin.Context) {
	currentUserId := c.GetString("userId")
	var req PostCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "Invalid request data"})
		return
	}

	id := uuid.New()
	_, err := masterDB.Exec(context.Background(),
		"INSERT INTO posts (id, text, author_user_id) VALUES ($1, $2, $3::uuid)",
		id, req.Text, currentUserId)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "Failed to create post"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"post_id": id.String()})
}

func getFeed(c *gin.Context) {
	currentUserId := c.GetString("userId")

	offsetStr := c.DefaultQuery("offset", "0")
	limitStr := c.DefaultQuery("limit", "10")
	offset, _ := strconv.Atoi(offsetStr)
	limit, _ := strconv.Atoi(limitStr)

	storage.mu.RLock()
	friends := storage.friendships[currentUserId]
	storage.mu.RUnlock()

	if friends == nil {
		c.JSON(http.StatusOK, []Post{})
		return
	}

	friendIds := make([]string, 0, len(friends))
	for f := range friends {
		friendIds = append(friendIds, f)
	}

	rows, err := slaveDB.Query(context.Background(),
		`SELECT id::text, text, author_user_id::text FROM posts 
		 WHERE author_user_id::text = ANY($1) 
		 ORDER BY id LIMIT $2 OFFSET $3`,
		friendIds, limit, offset)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "Database error"})
		return
	}
	defer rows.Close()

	var posts []Post
	for rows.Next() {
		p := Post{}
		err := rows.Scan(&p.ID, &p.Text, &p.AuthorUserID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"message": "Scan error"})
			return
		}
		posts = append(posts, p)
	}

	c.JSON(http.StatusOK, posts)
}

func sendMessage(c *gin.Context) {
	currentUserId := c.GetString("userId")
	toUserId := c.Param("user_id")

	var exists bool
	err := slaveDB.QueryRow(context.Background(), "SELECT EXISTS(SELECT 1 FROM users WHERE id::text = $1)", toUserId).Scan(&exists)
	if err != nil || !exists {
		c.JSON(http.StatusBadRequest, gin.H{"message": "Recipient not found"})
		return
	}

	bodyBytes, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "Failed to read request body"})
		return
	}

	path := fmt.Sprintf("/dialog/%s/send", toUserId)
	resp, err := makeDialogServiceRequest("POST", path, bodyBytes, currentUserId)
	if err != nil {
		log.Printf("Failed to call dialog service: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"message": "Dialog service unavailable", "code": 503})
		return
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "Failed to read dialog service response"})
		return
	}

	c.Data(resp.StatusCode, "application/json", respBody)
}

func getDialog(c *gin.Context) {
	currentUserId := c.GetString("userId")
	otherUserId := c.Param("user_id")

	path := fmt.Sprintf("/dialog/%s/list", otherUserId)
	resp, err := makeDialogServiceRequest("GET", path, nil, currentUserId)
	if err != nil {
		log.Printf("Failed to call dialog service: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"message": "Dialog service unavailable", "code": 503})
		return
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "Failed to read dialog service response"})
		return
	}

	c.Data(resp.StatusCode, "application/json", respBody)
}

func makeDialogServiceRequest(method, path string, body []byte, userId string) (*http.Response, error) {
	url := dialogServiceURL + path
	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, url, bodyReader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-User-ID", userId)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 10 * time.Second}
	return client.Do(req)
}

func hashPassword(password string) (string, error) {
	bytes, err := bcrypt.GenerateFromPassword([]byte(password), 14)
	return string(bytes), err
}

func checkPasswordHash(password, hash string) bool {
	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
	return err == nil
}

// insertLog (masterDB for write load)
func insertLog(c *gin.Context) {
	var req LogInsertRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "Invalid request data"})
		return
	}

	_, err := masterDB.Exec(context.Background(), "INSERT INTO logs (data) VALUES ($1)", req.Data)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "Insert error"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Log inserted"})
}

func setupRoutes() *gin.Engine {
	r := gin.Default()

	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok", "service": "monolith"})
	})

	r.POST("/log/insert", insertLog) // запись незащищенная (для эксперимента 2)

	r.POST("/login", login)
	r.POST("/user/register", register)
	r.GET("/user/get/:id", getUser)
	r.GET("/user/search", searchUsers)


	protected := r.Group("/")
	protected.Use(authMiddleware())
	{
		protected.PUT("/friend/set/:user_id", addFriend)
		protected.POST("/post/create", createPost)
		protected.GET("/post/feed", getFeed)
		protected.POST("/dialog/:user_id/send", sendMessage)
		protected.GET("/dialog/:user_id/list", getDialog)
	}

	return r
}
