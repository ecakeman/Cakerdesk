package events

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/ecakeman/cakerdesk/internal/apperr"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

const payloadLimit = 64 << 10

type Event struct {
	Type    string
	Attempt int32
	Payload json.RawMessage
}

type Row struct {
	Seq     int64
	Type    string
	Attempt int32
	Payload json.RawMessage
}

type Fence struct {
	WorkerID string
	Attempt  int32
}

type Service struct {
	pool *pgxpool.Pool
	rdb  *redis.Client
}

func New(pool *pgxpool.Pool, redisURL string) *Service {
	s := &Service{pool: pool}
	if redisURL == "" {
		return s
	}
	opt, err := redis.ParseURL(redisURL)
	if err != nil {
		slog.Error("redis", "err", err.Error())
		return s
	}
	opt.DialTimeout = 200 * time.Millisecond
	opt.MaxRetries = 0
	s.rdb = redis.NewClient(opt)
	return s
}

func (s *Service) Append(ctx context.Context, runID uuid.UUID, evs []Event, fence *Fence) (int64, int64, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback(ctx)
	first, last, err := s.AppendTx(ctx, tx, runID, evs, fence)
	if err != nil {
		return 0, 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, 0, err
	}
	s.Publish(runID, last)
	return first, last, nil
}

func (s *Service) AppendTx(ctx context.Context, tx pgx.Tx, runID uuid.UUID, evs []Event, fence *Fence) (int64, int64, error) {
	n := len(evs)
	if n == 0 {
		return 0, 0, apperr.New(http.StatusBadRequest, "invalid_request", "events")
	}
	// 序号在这条 UPDATE 里加上去。先读 last_seq 再加，并发追加会拿到同一个号。
	var last int64
	var err error
	if fence != nil {
		err = tx.QueryRow(ctx, `
			UPDATE runs
			SET last_seq = last_seq + $2
			WHERE id = $1 AND attempt = $3 AND lease_owner = $4 AND status = 'running'
			RETURNING last_seq`,
			runID, n, fence.Attempt, fence.WorkerID).Scan(&last)
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, 0, apperr.New(http.StatusConflict, "lease_lost", "租约条件不成立")
		}
	} else {
		err = tx.QueryRow(ctx, `
			UPDATE runs SET last_seq = last_seq + $2 WHERE id = $1 RETURNING last_seq`,
			runID, n).Scan(&last)
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, 0, apperr.New(http.StatusNotFound, "not_found", "run 不存在")
		}
	}
	if err != nil {
		return 0, 0, err
	}
	first := last - int64(n) + 1
	seqs := make([]int64, n)
	types := make([]string, n)
	attempts := make([]int32, n)
	payloads := make([]string, n)
	for i, ev := range evs {
		seqs[i] = first + int64(i)
		types[i] = ev.Type
		attempts[i] = ev.Attempt
		if len(ev.Payload) == 0 {
			payloads[i] = "{}"
		} else {
			payloads[i] = string(ev.Payload)
		}
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO run_events (run_id, seq, type, attempt, payload)
		SELECT $1, s, t, a, p::jsonb
		FROM unnest($2::bigint[], $3::text[], $4::int[], $5::text[]) AS x(s, t, a, p)`,
		runID, seqs, types, attempts, payloads)
	if err != nil {
		return 0, 0, err
	}
	return first, last, nil
}

func (s *Service) Publish(runID uuid.UUID, seq int64) {
	if s.rdb == nil || seq == 0 {
		return
	}
	// 库提交之后再发。用请求的 context 的话，客户端一断开，这次通知就丢了。
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := s.rdb.Publish(ctx, channel(runID), strconv.FormatInt(seq, 10)).Err()
	if err != nil {
		slog.Error("publish", "err", err.Error(), "run_id", runID.String(), "seq", seq)
	}
}

func (s *Service) List(ctx context.Context, runID uuid.UUID, after int64, limit int) ([]Row, error) {
	if limit < 1 || limit > 500 {
		limit = 500
	}
	rows, err := s.pool.Query(ctx, `
		SELECT seq, type, attempt, payload
		FROM run_events
		WHERE run_id = $1 AND seq > $2
		ORDER BY seq
		LIMIT $3`, runID, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Row, 0)
	for rows.Next() {
		var row Row
		if err := rows.Scan(&row.Seq, &row.Type, &row.Attempt, &row.Payload); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// B4 内核只能发 message.assistant。后面的类型等到那一步再放进来。
func CheckKernel(evs []Event) error {
	if len(evs) == 0 || len(evs) > 100 {
		return apperr.New(http.StatusBadRequest, "invalid_request", "events")
	}
	for _, ev := range evs {
		if ev.Type != "message.assistant" {
			return apperr.New(http.StatusBadRequest, "invalid_request", "type")
		}
		if len(ev.Payload) > payloadLimit {
			return apperr.New(http.StatusBadRequest, "invalid_request", "payload")
		}
	}
	return nil
}

func channel(id uuid.UUID) string {
	return "run:" + id.String()
}

func isTerminal(typ string) bool {
	return typ == "run.succeeded" || typ == "run.failed" || typ == "run.cancelled"
}

func writeSSE(w http.ResponseWriter, row Row) error {
	payload := row.Payload
	if len(payload) == 0 {
		payload = []byte("{}")
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, payload); err != nil {
		buf.Write(payload)
	}
	if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", row.Seq, row.Type, buf.Bytes()); err != nil {
		return err
	}
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	return nil
}
