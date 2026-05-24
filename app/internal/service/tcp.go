package service

import (
	"fmt"
	"log"
	"net"
	"os"
	"sync"
)

type TCPI interface {
	Start() error
	Stop()
}

type tcpService struct {
	listener net.Listener
	addr     string
	mu       sync.Mutex
	conns    map[string]net.Conn
}

func NewTCPService() TCPI {
	port := os.Getenv("TCP_PORT")
	if port == "" {
		port = "7000"
	}

	return &tcpService{
		addr:  ":" + port,
		conns: make(map[string]net.Conn),
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

	// TODO: Step 3 - JSON handshake (read token + port, reply with subdomain + url)
	// TODO: Step 4 - yamux.Server(conn, nil) wraps connection
	// TODO: Step 5 - Store session in tunnels map

	// For now, keep connection alive
	buf := make([]byte, 4096)
	for {
		_, err := conn.Read(buf)
		if err != nil {
			return
		}
	}
}
