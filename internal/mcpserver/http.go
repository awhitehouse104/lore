package mcpserver

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"time"

	"lore/internal/audit"
	"lore/internal/auth"
	"lore/internal/core"
	"lore/internal/serverconfig"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type HTTPService struct {
	config serverconfig.Config
	server *http.Server
	logger *slog.Logger
}

func NewHTTPService(service *core.Service, config serverconfig.Config, logger *slog.Logger) *HTTPService {
	if service == nil {
		panic("nil Lore service")
	}
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	authenticator := auth.NewBearerAuthenticator(config.BearerPrincipals)
	auditRecorder := audit.New(logger)
	protocolHandler := mcp.NewStreamableHTTPHandler(
		func(request *http.Request) *mcp.Server {
			principal, ok := auth.PrincipalFromContext(request.Context())
			if !ok {
				return nil
			}
			return NewWithContext(request.Context(), service, principal, logger).ProtocolServer()
		},
		&mcp.StreamableHTTPOptions{
			Stateless:                    true,
			JSONResponse:                 true,
			Logger:                       logger,
			MaxRequestBodyBytes:          config.Transport.RequestMaxBytes,
			PropagateRequestCancellation: true,
		},
	)
	endpoint := requestTimeoutMiddleware(
		authenticationMiddleware(
			authenticator,
			auditRecorder,
			concurrencyMiddleware(
				config.Transport.MaxConcurrentRequests,
				protocolHandler,
			),
		),
		config.Transport.RequestTimeout.Value(),
	)
	endpoint = originMiddleware(config.NormalizedOrigins, endpoint)
	root := exactRouteHandler(config.Endpoint, endpoint)
	httpService := &HTTPService{config: config, logger: logger}
	httpService.server = &http.Server{
		Addr:              config.Listen,
		Handler:           responseLimitMiddleware(config.Transport.ResponseMaxBytes, root),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       config.Transport.RequestTimeout.Value(),
		WriteTimeout:      config.Transport.RequestTimeout.Value() + 5*time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 * 1024,
	}
	return httpService
}

func (s *HTTPService) Handler() http.Handler {
	return s.server.Handler
}

func (s *HTTPService) ListenAndServe(ctx context.Context) error {
	listener, err := net.Listen("tcp", s.config.Listen)
	if err != nil {
		return fmt.Errorf("listen for MCP HTTP: %w", err)
	}
	if !s.config.IsLoopback() {
		s.logger.Warn("MCP HTTP plaintext non-loopback override enabled",
			"listen", s.config.Listen,
			"local_only_excluded", true,
		)
	}
	return s.Serve(ctx, listener)
}

func (s *HTTPService) Serve(ctx context.Context, listener net.Listener) error {
	result := make(chan error, 1)
	go func() {
		result <- s.server.Serve(listener)
	}()
	select {
	case err := <-result:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownContext, cancel := context.WithTimeout(context.Background(), s.config.Transport.ShutdownTimeout.Value())
		defer cancel()
		shutdownErr := s.server.Shutdown(shutdownContext)
		if shutdownErr != nil {
			_ = s.server.Close()
		}
		serveErr := <-result
		if shutdownErr != nil {
			return fmt.Errorf("graceful MCP HTTP shutdown: %w", shutdownErr)
		}
		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			return serveErr
		}
		return nil
	}
}

func exactRouteHandler(endpoint string, mcpHandler http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		setPrivateHeaders(writer.Header())
		if request.URL.RawQuery != "" || request.URL.RawPath != "" {
			http.NotFound(writer, request)
			return
		}
		switch request.URL.Path {
		case endpoint:
			mcpHandler.ServeHTTP(writer, request)
		case "/health/live", "/health/ready":
			healthHandler(writer, request)
		default:
			http.NotFound(writer, request)
		}
	})
}

func healthHandler(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writer.Header().Set("Allow", http.MethodGet)
		http.Error(writer, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(writer, "{\"status\":\"ok\"}\n")
}

func originMiddleware(allowed []string, next http.Handler) http.Handler {
	allowlist := make(map[string]struct{}, len(allowed))
	for _, origin := range allowed {
		allowlist[origin] = struct{}{}
	}
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		values := request.Header.Values("Origin")
		if len(values) == 0 {
			next.ServeHTTP(writer, request)
			return
		}
		if len(values) != 1 {
			http.Error(writer, "Forbidden", http.StatusForbidden)
			return
		}
		origin, err := serverconfig.NormalizeOrigin(values[0])
		if err != nil {
			http.Error(writer, "Forbidden", http.StatusForbidden)
			return
		}
		if _, ok := allowlist[origin]; !ok {
			http.Error(writer, "Forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(writer, request)
	})
}

func authenticationMiddleware(authenticator *auth.BearerAuthenticator, recorder *audit.Recorder, next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		principal, ok := authenticator.Authenticate(request.Header.Values("Authorization"))
		if !ok {
			recorder.AuthenticationDenied("http")
			writer.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(writer, "Unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(writer, request.WithContext(auth.WithPrincipal(request.Context(), principal)))
	})
}

func concurrencyMiddleware(maximum int, next http.Handler) http.Handler {
	slots := make(chan struct{}, maximum)
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
			next.ServeHTTP(writer, request)
		default:
			writer.Header().Set("Retry-After", "1")
			http.Error(writer, "Too Many Requests", http.StatusTooManyRequests)
		}
	})
}

func requestTimeoutMiddleware(next http.Handler, timeout time.Duration) http.Handler {
	return http.TimeoutHandler(next, timeout, "Request timed out.\n")
}

var errResponseTooLarge = errors.New("response exceeds configured limit")

type bufferedResponseWriter struct {
	header   http.Header
	body     bytes.Buffer
	status   int
	maximum  int64
	overflow bool
}

func newBufferedResponseWriter(maximum int64) *bufferedResponseWriter {
	return &bufferedResponseWriter{header: make(http.Header), maximum: maximum}
}

func (w *bufferedResponseWriter) Header() http.Header {
	return w.header
}

func (w *bufferedResponseWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}

func (w *bufferedResponseWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if w.overflow || int64(w.body.Len())+int64(len(data)) > w.maximum {
		w.overflow = true
		return 0, errResponseTooLarge
	}
	return w.body.Write(data)
}

func (w *bufferedResponseWriter) Flush() {}

func responseLimitMiddleware(maximum int64, next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		buffered := newBufferedResponseWriter(maximum)
		next.ServeHTTP(buffered, request)
		if buffered.overflow {
			setPrivateHeaders(writer.Header())
			http.Error(writer, "Response exceeded the configured limit.", http.StatusInternalServerError)
			return
		}
		for key, values := range buffered.header {
			writer.Header()[key] = append([]string(nil), values...)
		}
		setPrivateHeaders(writer.Header())
		status := buffered.status
		if status == 0 {
			status = http.StatusOK
		}
		writer.WriteHeader(status)
		_, _ = writer.Write(buffered.body.Bytes())
	})
}

func setPrivateHeaders(header http.Header) {
	header.Set("Cache-Control", "private, no-store")
	header.Set("Pragma", "no-cache")
	header.Set("X-Content-Type-Options", "nosniff")
}
