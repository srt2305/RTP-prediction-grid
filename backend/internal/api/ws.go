package api

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/coder/websocket"
)

// Fast enough to feel live, slow enough that a number does not change under
// the cursor between deciding to click and clicking.
const pushEvery = 500 * time.Millisecond

// stream pushes whole grid snapshots rather than deltas. At one symbol and a
// few dozen cells the payload is small, and it means a client that reconnects
// or joins late is immediately correct with no replay.
func (s *Server) stream(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		s.Log.Warn("websocket upgrade failed", "err", err)
		return
	}
	defer func() { _ = conn.CloseNow() }()

	ctx := r.Context()
	ticker := time.NewTicker(pushEvery)
	defer ticker.Stop()

	for {
		snap, err := s.snapshot(ctx)
		if err == nil {
			if writeErr := writeSnapshot(ctx, conn, snap); writeErr != nil {
				return
			}
		}

		select {
		case <-ticker.C:
		case <-ctx.Done():
			return
		}
	}
}

func writeSnapshot(ctx context.Context, conn *websocket.Conn, snap Snapshot) error {
	payload, err := json.Marshal(snap)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return conn.Write(ctx, websocket.MessageText, payload)
}
