package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"
)

type Repo struct {
	Name        string `json:"name"`
	FullName    string `json:"full_name"`
	Description string `json:"description"`
	URL         string `json:"html_url"`
	Language    string `json:"language"`
	Stars       int    `json:"stargazers_count"`
	Private     bool   `json:"private"`
}

type createRepoRequest struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Private     bool   `json:"private,omitempty"`
}

type updateRepoRequest struct {
	Description *string `json:"description,omitempty"`
	Private     *bool   `json:"private,omitempty"`
}

type Server struct {
	token  string
	owner  string
	client *http.Client
}

func newServer(ctx context.Context) (*Server, error) {
	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		return nil, errors.New("GITHUB_TOKEN not set")
	}
	s := &Server{
		token:  token,
		client: &http.Client{Timeout: 15 * time.Second},
	}
	owner, err := s.fetchLogin(ctx)
	if err != nil {
		return nil, fmt.Errorf("fetch authenticated user: %w", err)
	}
	s.owner = owner
	return s, nil
}

func (s *Server) do(ctx context.Context, method, url string, body any) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+s.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return s.client.Do(req)
}

func (s *Server) fetchLogin(ctx context.Context) (string, error) {
	resp, err := s.do(ctx, http.MethodGet, "https://api.github.com/user", nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close() // always close response body
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("github api: %s", resp.Status)
	}
	var u struct {
		Login string `json:"login"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&u); err != nil {
		return "", err
	}
	if u.Login == "" {
		return "", errors.New("empty login in /user response")
	}
	return u.Login, nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("encode response: %v", err)
	}
}

func proxyError(w http.ResponseWriter, resp *http.Response) {
	body, _ := io.ReadAll(resp.Body)
	http.Error(w, fmt.Sprintf("github api: %s: %s", resp.Status, string(body)), http.StatusBadGateway)
}

func (s *Server) listRepos(w http.ResponseWriter, r *http.Request) {
	var all []Repo
	for page := 1; ; page++ {
		url := fmt.Sprintf("https://api.github.com/user/repos?per_page=100&page=%d", page)
		resp, err := s.do(r.Context(), http.MethodGet, url, nil)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		if resp.StatusCode != http.StatusOK {
			proxyError(w, resp)
			resp.Body.Close()
			return
		}
		var batch []Repo
		if err := json.NewDecoder(resp.Body).Decode(&batch); err != nil {
			resp.Body.Close()
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		resp.Body.Close()
		all = append(all, batch...)
		if len(batch) < 100 {
			break
		}
	}
	writeJSON(w, http.StatusOK, all)
}

func (s *Server) createRepo(w http.ResponseWriter, r *http.Request) {
	var in createRepoRequest
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
		return
	}
	if in.Name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}

	resp, err := s.do(r.Context(), http.MethodPost, "https://api.github.com/user/repos", in)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		proxyError(w, resp)
		return
	}
	var repo Repo
	if err := json.NewDecoder(resp.Body).Decode(&repo); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, repo)
}

func (s *Server) getRepo(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s", s.owner, name)
	resp, err := s.do(r.Context(), http.MethodGet, url, nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		http.Error(w, "repo not found", http.StatusNotFound)
		return
	}
	if resp.StatusCode != http.StatusOK {
		proxyError(w, resp)
		return
	}
	var repo Repo
	if err := json.NewDecoder(resp.Body).Decode(&repo); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, repo)
}

func (s *Server) updateRepo(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var in updateRepoRequest
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
		return
	}
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s", s.owner, name)
	resp, err := s.do(r.Context(), http.MethodPatch, url, in)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		http.Error(w, "repo not found", http.StatusNotFound)
		return
	}
	if resp.StatusCode != http.StatusOK {
		proxyError(w, resp)
		return
	}
	var repo Repo
	if err := json.NewDecoder(resp.Body).Decode(&repo); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, repo)
}

func (s *Server) deleteRepo(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s", s.owner, name)
	resp, err := s.do(r.Context(), http.MethodDelete, url, nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNoContent:
		w.WriteHeader(http.StatusNoContent)
	case http.StatusNotFound:
		http.Error(w, "repo not found", http.StatusNotFound)
	case http.StatusForbidden:
		proxyError(w, resp)
	default:
		proxyError(w, resp)
	}
}

func main() {
	if err := loadEnvFile(".env"); err != nil {
		log.Fatalf("load .env: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	s, err := newServer(ctx)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("authenticated as %s", s.owner)

	auth, err := newAuthenticator()
	if err != nil {
		log.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /login", auth.login)
	mux.HandleFunc("GET /repos", auth.requireAuth(s.listRepos))
	mux.HandleFunc("POST /repos", auth.requireAuth(s.createRepo))
	mux.HandleFunc("GET /repos/{name}", auth.requireAuth(s.getRepo))
	mux.HandleFunc("PATCH /repos/{name}", auth.requireAuth(s.updateRepo))
	mux.HandleFunc("DELETE /repos/{name}", auth.requireAuth(s.deleteRepo))

	addr := ":8080"
	log.Printf("listening on %s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatal(err)
	}
}
