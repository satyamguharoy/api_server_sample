package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

type Authenticator struct {
	secret   []byte
	user     string
	pass     string
	tokenTTL time.Duration
}

func newAuthenticator() (*Authenticator, error) {
	secret := os.Getenv("JWT_SECRET")
	if secret == "" {
		return nil, errors.New("JWT_SECRET not set")
	}
	user := os.Getenv("DEMO_USER")
	pass := os.Getenv("DEMO_PASS")
	if user == "" || pass == "" {
		return nil, errors.New("DEMO_USER and DEMO_PASS must be set")
	}
	return &Authenticator{
		secret:   []byte(secret),
		user:     user,
		pass:     pass,
		tokenTTL: time.Hour,
	}, nil
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type loginResponse struct {
	Token string `json:"token"`
}

func (a *Authenticator) login(w http.ResponseWriter, r *http.Request) {
	var in loginRequest
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
		return
	}
	if in.Username != a.user || in.Password != a.pass {
		http.Error(w, "invalid credentials", http.StatusUnauthorized)
		return
	}
	now := time.Now()
	claims := jwt.RegisteredClaims{
		Subject:   in.Username,
		IssuedAt:  jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(now.Add(a.tokenTTL)),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString(a.secret)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, loginResponse{Token: signed})
}

func (a *Authenticator) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		if !strings.HasPrefix(header, "Bearer ") {
			http.Error(w, "missing bearer token", http.StatusUnauthorized)
			return
		}
		raw := strings.TrimPrefix(header, "Bearer ")
		token, err := jwt.Parse(raw, func(t *jwt.Token) (any, error) {
			if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
			}
			return a.secret, nil
		})
		if err != nil || !token.Valid {
			http.Error(w, "invalid token", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}
