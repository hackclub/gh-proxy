package server

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base32"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"log"
	"time"
)

func (s *Server) handleGitHubLogin(w http.ResponseWriter, r *http.Request) {
	redir := s.cfg.BaseURL + "/auth/github/callback"
	// generate state and set cookie
	st := make([]byte, 16)
	_, _ = rand.Read(st)
	state := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(st)
	http.SetCookie(w, &http.Cookie{
		Name:     "oauth_state",
		Value:    state,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   strings.HasPrefix(s.cfg.BaseURL, "https://"),
		MaxAge:   300,
	})
	u := fmt.Sprintf(
		"https://github.com/login/oauth/authorize?client_id=%s&redirect_uri=%s&scope=read:user&state=%s",
		url.QueryEscape(s.cfg.GithubClientID),
		url.QueryEscape(redir),
		url.QueryEscape(state),
	)
	log.Printf("oauth: redirecting to GitHub for login")
	http.Redirect(w, r, u, http.StatusFound)
}

type ghTokenResp struct {
	AccessToken string `json:"access_token"`
	Scope       string `json:"scope"`
	TokenType   string `json:"token_type"`
}

type ghUser struct{ Login string `json:"login"` }

func (s *Server) handleGitHubCallback(w http.ResponseWriter, r *http.Request) {
	if e := r.URL.Query().Get("error"); e != "" {
		http.Error(w, "oauth error: "+e, http.StatusBadRequest)
		return
	}
	state := r.URL.Query().Get("state")
	c, err := r.Cookie("oauth_state")
	if err != nil || state == "" || subtle.ConstantTimeCompare([]byte(state), []byte(c.Value)) != 1 {
		http.Error(w, "invalid oauth state", http.StatusBadRequest)
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		http.Error(w, "missing code", http.StatusBadRequest)
		return
	}

	form := url.Values{
		"client_id":     {s.cfg.GithubClientID},
		"client_secret": {s.cfg.GithubClientSecret},
		"code":          {code},
	}
	req, _ := http.NewRequest("POST", "https://github.com/login/oauth/access_token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		http.Error(w, fmt.Sprintf("oauth token exchange failed (%d): %s", resp.StatusCode, string(b)), http.StatusBadGateway)
		return
	}
	var tok ghTokenResp
	if err := json.Unmarshal(b, &tok); err != nil {
		http.Error(w, "failed to parse token JSON: "+err.Error()+" | body: "+string(b), http.StatusInternalServerError)
		return
	}
	if tok.AccessToken == "" {
		http.Error(w, "no token", http.StatusBadRequest)
		return
	}

	req2, _ := http.NewRequest("GET", "https://api.github.com/user", nil)
	req2.Header.Set("Accept", "application/vnd.github+json")
	req2.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	uresp, err := client.Do(req2)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer uresp.Body.Close()
	var user ghUser
	if err := json.NewDecoder(uresp.Body).Decode(&user); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if user.Login == "" {
		http.Error(w, "no user", http.StatusBadRequest)
		return
	}
	_, err = s.pool.Exec(r.Context(), `INSERT INTO donated_tokens(github_user, token, revoked, scopes, last_ok_at) VALUES($1,$2,false,$3,now())
	ON CONFLICT (github_user) DO UPDATE SET token=EXCLUDED.token, revoked=false, scopes=EXCLUDED.scopes, last_ok_at=now()`, user.Login, tok.AccessToken, tok.Scope)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	log.Printf("oauth: token stored for @%s", user.Login)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
