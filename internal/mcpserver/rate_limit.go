package mcpserver

import (
	"net/http"
	"strconv"
	"time"

	"lore/internal/auth"
	"lore/internal/serverconfig"

	"golang.org/x/time/rate"
)

type principalRateLimiter struct {
	buckets    map[string]*rate.Limiter
	now        func() time.Time
	retryAfter string
}

func newPrincipalRateLimiter(
	principals []auth.BearerPrincipal,
	config serverconfig.RateLimitConfig,
	now func() time.Time,
) *principalRateLimiter {
	if now == nil {
		now = time.Now
	}
	refill := rate.Limit(float64(config.RequestsPerMinute) / 60)
	buckets := make(map[string]*rate.Limiter, len(principals))
	for _, entry := range principals {
		buckets[entry.Principal.ID] = rate.NewLimiter(refill, config.BurstRequests)
	}
	return &principalRateLimiter{
		buckets:    buckets,
		now:        now,
		retryAfter: strconv.Itoa(retryAfterSeconds(config.RequestsPerMinute)),
	}
}

func (l *principalRateLimiter) allow(principalID string) (allowed, known bool) {
	if l == nil {
		return false, false
	}
	bucket, ok := l.buckets[principalID]
	if !ok {
		return false, false
	}
	return bucket.AllowN(l.now(), 1), true
}

func retryAfterSeconds(requestsPerMinute int) int {
	if requestsPerMinute <= 0 {
		return 1
	}
	seconds := (60 + requestsPerMinute - 1) / requestsPerMinute
	if seconds < 1 {
		return 1
	}
	return seconds
}

func principalRateLimitMiddleware(limiter *principalRateLimiter, next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		principal, ok := auth.PrincipalFromContext(request.Context())
		if !ok {
			http.Error(writer, "Internal Server Error", http.StatusInternalServerError)
			return
		}
		allowed, known := limiter.allow(principal.ID)
		if !known {
			http.Error(writer, "Internal Server Error", http.StatusInternalServerError)
			return
		}
		if !allowed {
			writer.Header().Set("Retry-After", limiter.retryAfter)
			http.Error(writer, "Too Many Requests", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(writer, request)
	})
}
