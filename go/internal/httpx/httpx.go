package httpx

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/ecakeman/cakerdesk/internal/apperr"
)

const maxBody = 1 << 20

func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("write json", "err", err.Error())
	}
}

func WriteError(w http.ResponseWriter, err error) {
	var ae *apperr.Error
	if errors.As(err, &ae) && ae != nil {
		WriteJSON(w, ae.Status, map[string]any{
			"error": map[string]string{"code": ae.Code, "message": ae.Msg},
		})
		return
	}
	slog.Error("internal", "err", err.Error())
	WriteJSON(w, http.StatusInternalServerError, map[string]any{
		"error": map[string]string{"code": "internal", "message": "内部错误"},
	})
}

func DecodeJSON(r *http.Request, dst any) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	if err != nil {
		return apperr.New(http.StatusBadRequest, "invalid_json", "读取请求体失败")
	}
	if len(body) > maxBody {
		return apperr.New(http.StatusBadRequest, "invalid_json", "请求体超过 1MiB")
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return apperr.New(http.StatusBadRequest, "invalid_json", "请求体不是合法 JSON")
	}
	var extra struct{}
	if err := dec.Decode(&extra); err != io.EOF {
		return apperr.New(http.StatusBadRequest, "invalid_json", "请求体包含多余内容")
	}
	return nil
}

func LogRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		slog.Info("http", "method", r.Method, "path", r.URL.Path, "status", rec.status, "dur_ms", time.Since(start).Milliseconds())
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
