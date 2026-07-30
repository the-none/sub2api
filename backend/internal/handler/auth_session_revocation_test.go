//go:build unit

package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestAuthHandlerRevokeAllSessionsInvalidatesAccessTokens(t *testing.T) {
	gin.SetMode(gin.TestMode)

	repo := &userHandlerRepoStub{
		user: &service.User{
			ID:           29,
			Email:        "session@example.com",
			Username:     "session-user",
			Role:         service.RoleUser,
			Status:       service.StatusActive,
			TokenVersion: 7,
		},
	}
	refreshTokenCache := &userHandlerRefreshTokenCacheStub{}
	cfg := &config.Config{
		JWT: config.JWTConfig{
			Secret:     "test-secret",
			ExpireHour: 1,
		},
	}
	authService := service.NewAuthService(nil, repo, nil, refreshTokenCache, cfg, nil, nil, nil, nil, nil, nil, nil, nil)
	handler := &AuthHandler{authService: authService}
	oldToken, err := authService.GenerateToken(context.Background(), repo.user)
	require.NoError(t, err)
	oldClaims, err := authService.ValidateToken(oldToken)
	require.NoError(t, err)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/auth/revoke-all-sessions", nil)
	c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 29})

	handler.RevokeAllSessions(c)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, []int64{29}, refreshTokenCache.revokedUserIDs)
	require.Equal(t, int64(8), repo.user.TokenVersion)
	loaded, err := service.NewUserService(repo, nil, nil, nil).GetByID(context.Background(), repo.user.ID)
	require.NoError(t, err)
	require.NotEqual(t, oldClaims.TokenVersion, loaded.TokenVersion)

	var resp struct {
		Code int `json:"code"`
		Data struct {
			Message string `json:"message"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &resp))
	require.Equal(t, 0, resp.Code)
	require.Equal(t, "All sessions have been revoked. Please log in again.", resp.Data.Message)
}

func TestAuthHandlerRevokeAllSessionsReportsRefreshRevocationFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)

	repo := &userHandlerRepoStub{user: &service.User{
		ID: 30, Email: "session-error@example.com", Role: service.RoleUser,
		Status: service.StatusActive, TokenVersion: 2,
	}}
	refreshTokenCache := &userHandlerRefreshTokenCacheStub{deleteUserErr: errors.New("redis down")}
	authService := service.NewAuthService(nil, repo, nil, refreshTokenCache, &config.Config{
		JWT: config.JWTConfig{Secret: "test-secret", ExpireHour: 1},
	}, nil, nil, nil, nil, nil, nil, nil, nil)
	handler := &AuthHandler{authService: authService}

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/auth/revoke-all-sessions", nil)
	c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 30})

	handler.RevokeAllSessions(c)

	require.Equal(t, http.StatusInternalServerError, recorder.Code)
	require.Equal(t, int64(3), repo.user.TokenVersion, "access tokens remain revoked even if refresh cleanup fails")
	require.Equal(t, []int64{30}, refreshTokenCache.revokedUserIDs)
}
