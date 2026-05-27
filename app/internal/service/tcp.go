package service

import (
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"sync"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
)

type TCPI interface {
	Start() error
	Stop()
}

type tunnelRegistrationRequest struct {
	Type string `json:"type"`
	Port string `json:"port"`
}

type tunnelRegistrationResponse struct {
	Subdomain string `json:"subdomain"`
	URL       string `json:"url"`
	Error     string `json:"error,omitempty"`
}

type tcpService struct {
	listener net.Listener
	addr     string
	mu       sync.Mutex
	conns    map[string]net.Conn
	app      core.App
	domain   string
}

func NewTCPService(app core.App) TCPI {
	port := os.Getenv("TCP_PORT")
	if port == "" {
		port = "7000"
	}

	domain := os.Getenv("GOPORT_DOMAIN")
	if domain == "" {
		domain = "goport.uz"
	}

	return &tcpService{
		addr:   ":" + port,
		conns:  make(map[string]net.Conn),
		app:    app,
		domain: domain,
	}
}

func (t *tcpService) Start() error {
	listener, err := net.Listen("tcp", t.addr)
	if err != nil {
		return fmt.Errorf("failed to start TCP listener on %s: %w", t.addr, err)
	}
	t.listener = listener

	log.Printf("TCP tunnel listener started on %s", t.addr)

	go t.acceptLoop()
	return nil
}

func (t *tcpService) Stop() {
	if t.listener != nil {
		t.listener.Close()
	}

	t.mu.Lock()
	for addr, conn := range t.conns {
		conn.Close()
		delete(t.conns, addr)
	}
	t.mu.Unlock()

	log.Printf("TCP tunnel listener stopped")
}

func (t *tcpService) acceptLoop() {
	for {
		conn, err := t.listener.Accept()
		if err != nil {
			log.Printf("TCP accept error: %v", err)
			return
		}

		remoteAddr := conn.RemoteAddr().String()
		log.Printf("new CLI connection from %s", remoteAddr)

		t.mu.Lock()
		t.conns[remoteAddr] = conn
		t.mu.Unlock()

		go t.handleConnection(conn)
	}
}

func (t *tcpService) handleConnection(conn net.Conn) {
	defer func() {
		remoteAddr := conn.RemoteAddr().String()
		conn.Close()

		t.mu.Lock()
		delete(t.conns, remoteAddr)
		t.mu.Unlock()

		log.Printf("CLI disconnected: %s", remoteAddr)
	}()

	var req tunnelRegistrationRequest
	if err := json.NewDecoder(conn).Decode(&req); err != nil {
		log.Printf("failed to read tunnel registration: %v", err)
		return
	}
	if req.Port == "" {
		log.Printf("tunnel registration missing port")
		t.sendRegistrationError(conn, "missing port")
		return
	}

	subdomain, err := t.createTunnel()
	if err != nil {
		log.Printf("failed to create tunnel: %v", err)
		t.sendRegistrationError(conn, fmt.Sprintf("failed to create tunnel: %v", err))
		return
	}

	resp := tunnelRegistrationResponse{
		Subdomain: subdomain,
		URL:       fmt.Sprintf("https://%s.%s", subdomain, t.domain),
	}
	if err := json.NewEncoder(conn).Encode(resp); err != nil {
		log.Printf("failed to send tunnel registration response: %v", err)
		return
	}

	log.Printf("registered %s tunnel %s -> localhost:%s", req.Type, resp.URL, req.Port)

	// For now, keep connection alive
	buf := make([]byte, 4096)
	for {
		_, err := conn.Read(buf)
		if err != nil {
			return
		}
	}
}

func (t *tcpService) sendRegistrationError(conn net.Conn, msg string) {
	if err := json.NewEncoder(conn).Encode(tunnelRegistrationResponse{Error: msg}); err != nil {
		log.Printf("failed to send tunnel registration error: %v", err)
	}
}

func (t *tcpService) createTunnel() (string, error) {
	collection, err := t.app.FindCollectionByNameOrId("tunnels")
	if err != nil {
		return "", err
	}

	const userID = "5743847505m28jb"
	subdomain, err := t.generateUniqueSubdomain()
	if err != nil {
		return "", err
	}

	record := core.NewRecord(collection)
	record.Set("user", userID)
	record.Set("subdomain", subdomain)
	record.Set("is_custom", false)

	if err := t.app.Save(record); err != nil {
		return "", err
	}

	return subdomain, nil
}

func (t *tcpService) generateUniqueSubdomain() (string, error) {
	for i := 0; i < 10; i++ {
		subdomain, err := randomSubdomain(6)
		if err != nil {
			return "", err
		}

		_, err = t.app.FindFirstRecordByFilter("tunnels", "subdomain = {:subdomain}", dbx.Params{
			"subdomain": subdomain,
		})
		if errors.Is(err, sql.ErrNoRows) {
			return subdomain, nil
		}
		if err != nil {
			return "", err
		}
	}

	return "", fmt.Errorf("failed to generate unique subdomain")
}

func randomSubdomain(length int) (string, error) {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"

	bytes := make([]byte, length)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}

	for i := range bytes {
		bytes[i] = alphabet[int(bytes[i])%len(alphabet)]
	}

	return string(bytes), nil
}
