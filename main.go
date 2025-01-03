package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

var (
	upgrader       = websocket.Upgrader{CheckOrigin: checkOrigin}
	allowedOrigins = map[string]struct{}{}
	mu             sync.RWMutex
)

type Config struct {
	AllowedOrigins []string `json:"allowed_origins"`
	Port           int      `json:"port"`
	RateLimit      int      `json:"rate_limit"`
	Timeout        int      `json:"timeout"`
}

func loadConfig(filename string) (Config, error) {
	var config Config
	data, err := os.ReadFile(filename)
	if err != nil {
		return config, err
	}
	err = json.Unmarshal(data, &config)
	return config, err
}

func checkOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")

	mu.RLock()
	defer mu.RUnlock()

	if _, ok := allowedOrigins[origin]; ok {
		return true
	}

	log.Printf("Connection from disallowed origin: %s", origin)
	return false
}

type Client struct {
	conn         *websocket.Conn
	send         chan []byte
	lastActive   time.Time
	messageCount int
	rateLimit    int
}

type Hub struct {
	clients   map[*Client]bool
	broadcast chan []byte
	mu        sync.Mutex
}

var hub = Hub{
	clients:   make(map[*Client]bool),
	broadcast: make(chan []byte),
}

func (h *Hub) run() {
	for {
		msg := <-h.broadcast
		h.mu.Lock()
		for client := range h.clients {
			select {
			case client.send <- msg:
			default:
				close(client.send)
				delete(h.clients, client)
			}
		}
		h.mu.Unlock()
	}
}

func (c *Client) read(timeout time.Duration) {
	defer func() {
		c.conn.Close()
	}()
	for {
		c.conn.SetReadDeadline(time.Now().Add(timeout))
		_, msg, err := c.conn.ReadMessage()
		if err != nil {
			break
		}

		c.messageCount++
		if c.messageCount > c.rateLimit {
			log.Println("Rate limit exceeded for client, closing connection.")
			break
		}

		hub.broadcast <- msg
		c.lastActive = time.Now()
	}
}

func (c *Client) write() {
	defer func() {
		c.conn.Close()
	}()
	for msg := range c.send {
		if err := c.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
			break
		}
	}
}

func handleConnection(w http.ResponseWriter, r *http.Request, rateLimit int, timeout time.Duration) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("Error during connection upgrade:", err)
		return
	}
	client := &Client{conn: conn, send: make(chan []byte), lastActive: time.Now(), rateLimit: rateLimit}
	hub.mu.Lock()
	hub.clients[client] = true
	hub.mu.Unlock()

	go client.read(timeout)
	go client.write()
}

var config Config

func main() {
	var err error
	config, err = loadConfig("config.json")
	if err != nil {
		log.Fatalf("Error loading config: %v", err)
	}

	mu.Lock()
	for _, origin := range config.AllowedOrigins {
		allowedOrigins[origin] = struct{}{}
	}
	mu.Unlock()

	go hub.run()

	http.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		handleConnection(w, r, config.RateLimit, time.Duration(config.Timeout)*time.Second)
	})

	addr := fmt.Sprintf(":%d", config.Port)
	log.Printf("Запуск сервера на порту %d...", config.Port)
	if err := http.ListenAndServe(addr, nil); err != nil {
		log.Fatalf("Ошибка при запуске сервера: %v", err)
	}
}
