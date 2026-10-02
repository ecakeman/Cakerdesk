package events

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
)

func (s *Service) Stream(ctx context.Context, w http.ResponseWriter, runID uuid.UUID, last int64) error {
	// 先订阅再回放。反过来的话，回放结束到订阅生效之间的事件没有人收。
	live, stop, subErr := s.subscribe(ctx, runID)
	if stop != nil {
		defer stop()
	}
	if subErr != nil {
		live = nil
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	done, err := s.replay(ctx, w, runID, &last)
	if err != nil || done {
		return err
	}
	heart := time.NewTicker(15 * time.Second)
	defer heart.Stop()
	poll := time.NewTicker(time.Second)
	defer poll.Stop()
	if live != nil {
		poll.Stop()
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-heart.C:
			if _, err := fmt.Fprintf(w, ": ping\n\n"); err != nil {
				return nil
			}
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		case seq, ok := <-live:
			if !ok {
				live = nil
				poll.Reset(time.Second)
				continue
			}
			if seq <= last {
				continue
			}
			// 通知里只有序号。正文在库里，下一号和缺口都从这里补。
			done, err := s.replay(ctx, w, runID, &last)
			if err != nil || done {
				return err
			}
		case <-poll.C:
			if live != nil {
				continue
			}
			done, err := s.replay(ctx, w, runID, &last)
			if err != nil || done {
				return err
			}
		}
	}
}

func (s *Service) replay(ctx context.Context, w http.ResponseWriter, runID uuid.UUID, last *int64) (bool, error) {
	for {
		rows, err := s.List(ctx, runID, *last, 500)
		if err != nil {
			return false, err
		}
		for _, row := range rows {
			if err := writeSSE(w, row); err != nil {
				return false, err
			}
			*last = row.Seq
			if isTerminal(row.Type) {
				return true, nil
			}
		}
		if len(rows) < 500 {
			return false, nil
		}
	}
}

func (s *Service) subscribe(ctx context.Context, runID uuid.UUID) (<-chan int64, func(), error) {
	if s.rdb == nil {
		return nil, nil, nil
	}
	sub := s.rdb.Subscribe(ctx, channel(runID))
	stop := func() { _ = sub.Close() }
	receiveCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if _, err := sub.Receive(receiveCtx); err != nil {
		stop()
		return nil, nil, err
	}
	out := make(chan int64, 16)
	go func() {
		defer close(out)
		for msg := range sub.Channel() {
			seq, err := strconv.ParseInt(msg.Payload, 10, 64)
			if err != nil {
				continue
			}
			select {
			case out <- seq:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, stop, nil
}
