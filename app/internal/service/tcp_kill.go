package service

import (
	"database/sql"
	"errors"
	"log"
	"net"
	"time"

	"github.com/hashicorp/yamux"
	"github.com/pocketbase/dbx"
	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/model"
)

// stoppedMessage refuses a reconnect right after a stop from the dashboard.
// It contains "subdomain cannot", so CLIs 0.1.8+ stop instead of resuming.
const stoppedMessage = "subdomain cannot be resumed: the tunnel was stopped from the dashboard"

// stopHold is how long a stopped subdomain stays refused to its account.
const stopHold = time.Minute

// revokedTokenHold is how long a deleted token's in-flight registrations are
// refused. Verify already refuses it once the row is gone, so this only
// covers a registration that verified the token just before.
const revokedTokenHold = time.Minute

func stopKey(userID, subdomain string) string { return userID + "\x00" + subdomain }

// markStopped refuses subdomain to userID for stopHold. Must hold t.mu.
func (t *tcpService) markStoppedLocked(userID, subdomain string, now time.Time) {
	for key, until := range t.stopped {
		if !now.Before(until) {
			delete(t.stopped, key)
		}
	}
	t.stopped[stopKey(userID, subdomain)] = now.Add(stopHold)
}

// recentlyStopped reports whether userID stopped subdomain from the dashboard
// within stopHold.
func (t *tcpService) recentlyStopped(userID, subdomain string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	until, ok := t.stopped[stopKey(userID, subdomain)]
	return ok && time.Now().Before(until)
}

// CloseSessionsForToken ends every tunnel opened with the token whose row id
// is tokenID, including registrations still in their handshake, and returns
// how many it closed. Called when a token is deleted; reconnects then fail
// with "authentication required".
func (t *tcpService) CloseSessionsForToken(tokenID string) int {
	if tokenID == "" {
		return 0
	}
	now := time.Now()
	var sessions []*yamux.Session
	var conns []net.Conn
	t.mu.Lock()
	for key, until := range t.revokedTokens {
		if !now.Before(until) {
			delete(t.revokedTokens, key)
		}
	}
	t.revokedTokens[tokenID] = now.Add(revokedTokenHold)
	for subdomain, id := range t.sessionTokens {
		if id != tokenID {
			continue
		}
		if session := t.sessions[subdomain]; session != nil {
			sessions = append(sessions, session)
		} else if conn := t.owners[subdomain]; conn != nil {
			conns = append(conns, conn)
		}
	}
	t.mu.Unlock()

	for _, session := range sessions {
		_ = session.Close()
	}
	for _, conn := range conns {
		_ = conn.Close()
	}
	if closed := len(sessions) + len(conns); closed > 0 {
		log.Printf("closed %d tunnel(s) of deleted token %s", closed, tokenID)
		return closed
	}
	return 0
}

// tokenRevokedLocked reports whether tokenID was deleted moments ago. Must
// hold t.mu.
func (t *tcpService) tokenRevokedLocked(tokenID string) bool {
	until, ok := t.revokedTokens[tokenID]
	return ok && time.Now().Before(until)
}

// tokenID returns the row id of a CLI token, or "" when it can't be read.
func (t *tcpService) tokenID(token string) string {
	if t.app == nil || token == "" {
		return ""
	}
	record, err := t.app.FindFirstRecordByFilter(model.TokensCollection, "token = {:token}", dbx.Params{"token": token})
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			log.Printf("could not look up a token id: %v", err)
		}
		return ""
	}
	return record.Id
}
