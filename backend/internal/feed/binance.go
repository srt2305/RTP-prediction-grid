package feed

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"
)

const (
	binanceStreamURL = "wss://stream.binance.com:9443/ws"
	reconnectMin     = 500 * time.Millisecond
	reconnectMax     = 30 * time.Second
)

type Binance struct {
	Log *slog.Logger
}

type binanceTrade struct {
	Price     string `json:"p"`
	TradeTime int64  `json:"T"`
}

// A dropped connection stalls settlement rather than breaking it: the oracle
// refuses to fix a price it has no ticks for.
func (b Binance) Subscribe(ctx context.Context, symbol string) (<-chan Tick, error) {
	url := fmt.Sprintf("%s/%s@trade", binanceStreamURL, strings.ToLower(symbol))

	ticks := make(chan Tick, 256)
	go func() {
		defer close(ticks)

		backoff := reconnectMin
		for ctx.Err() == nil {
			if err := b.stream(ctx, url, symbol, ticks); err != nil && ctx.Err() == nil {
				b.Log.Warn("feed disconnected", "symbol", symbol, "retry_in", backoff, "err", err)
				select {
				case <-time.After(backoff):
					backoff = min(backoff*2, reconnectMax)
				case <-ctx.Done():
					return
				}
				continue
			}
			backoff = reconnectMin
		}
	}()
	return ticks, nil
}

func (b Binance) stream(ctx context.Context, url, symbol string, ticks chan<- Tick) error {
	conn, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		return fmt.Errorf("dial %s: %w", url, err)
	}
	defer func() { _ = conn.CloseNow() }()

	conn.SetReadLimit(1 << 20)
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return fmt.Errorf("read: %w", err)
		}

		var trade binanceTrade
		if err := json.Unmarshal(data, &trade); err != nil {
			return fmt.Errorf("decode trade: %w", err)
		}
		price, err := strconv.ParseFloat(trade.Price, 64)
		if err != nil {
			return fmt.Errorf("parse price %q: %w", trade.Price, err)
		}

		select {
		case ticks <- Tick{Symbol: symbol, Price: price, At: time.UnixMilli(trade.TradeTime).UTC()}:
		case <-ctx.Done():
			return nil
		}
	}
}
