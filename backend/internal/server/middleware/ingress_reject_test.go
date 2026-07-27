package middleware

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	ippkg "github.com/Wei-Shaw/sub2api/internal/pkg/ip"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type ingressRejectRecorderStub struct {
	mu       sync.Mutex
	calls    int
	clientIP string
}

func (r *ingressRejectRecorderStub) RecordIngressReject(_, _, _, clientIP string, _, _ int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	r.clientIP = clientIP
}

func TestNormalizeIngressRejectIP(t *testing.T) {
	require.Equal(t, "2001:db8:abcd:1234::", normalizeIngressRejectIP("2001:db8:abcd:1234:ffff::1"))
	require.Equal(t, "192.0.2.4", normalizeIngressRejectIP("::ffff:192.0.2.4"))
	require.Equal(t, "0.0.0.0", normalizeIngressRejectIP("not-an-ip"))
}

func TestInvalidAuthClientKeyIgnoresUntrustedForwardedHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	require.NoError(t, router.SetTrustedProxies(nil))
	router.Use(func(c *gin.Context) {
		ippkg.SetForwardedIPSettings(c, true, nil)
		c.Next()
	})
	router.GET("/key", func(c *gin.Context) {
		c.String(http.StatusOK, invalidAuthClientKey(c))
	})

	for _, xff := range []string{"10.0.0.7", "198.51.100.8"} {
		request := httptest.NewRequest(http.MethodGet, "/key", nil)
		request.RemoteAddr = "203.0.113.10:1234"
		request.Header.Set("X-Forwarded-For", xff)
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		require.Equal(t, http.StatusOK, recorder.Code)
		require.Equal(t, "203.0.113.10", recorder.Body.String())
	}
}

func TestLoggerRecordsIngressRejectOnce(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := &ingressRejectRecorderStub{}
	SetIngressRejectRecorder(recorder)
	t.Cleanup(func() { SetIngressRejectRecorder(nil) })
	router := gin.New()
	router.Use(Logger())
	router.GET("/v1/messages", func(c *gin.Context) {
		MarkIngressRejected(c, IngressRejectInvalidAPIKey)
		c.Status(http.StatusUnauthorized)
	})
	request := httptest.NewRequest(http.MethodGet, "/v1/messages", nil)
	request.RemoteAddr = "[2001:db8:abcd:1234:ffff::1]:1234"
	router.ServeHTTP(httptest.NewRecorder(), request)
	recorder.mu.Lock()
	require.Equal(t, 1, recorder.calls)
	require.Equal(t, "2001:db8:abcd:1234::", recorder.clientIP)
	recorder.mu.Unlock()
}
