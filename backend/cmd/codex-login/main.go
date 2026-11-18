// SPDX-License-Identifier: AGPL-3.0-only

// Command codex-login pairs a user's browser authorization with one bot.
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"time"

	"github.com/dunkeltech/botbox/backend/internal/codexplan"
	apigen "github.com/dunkeltech/botbox/backend/internal/gen/api"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	input := bufio.NewReader(os.Stdin)
	fmt.Print("bot.Box URL from the connection card: ")
	base, _ := input.ReadString('\n')
	target, err := controlPlaneURL(strings.TrimSpace(base))
	if err != nil {
		return err
	}
	fmt.Print("One-time pairing code: ")
	code, err := readPairing(ctx, input)
	if err != nil {
		return err
	}
	pair, ok := codexplan.ParsePairing(code)
	if !ok {
		return errors.New("invalid pairing code")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	grant, err := authorize(ctx, pair)
	if err != nil {
		return err
	}
	request, err := json.Marshal(apigen.CompleteCodexPlanRequest{Pairing: code, Code: grant.Code, ClientId: grant.ClientID,
		Verifier: grant.Verifier, Nonce: grant.Nonce, Redirect: grant.Redirect})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target.String(), bytes.NewReader(request))
	if err != nil {
		return errors.New("could not build the bot.Box pairing request")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-BotBox-CSRF", "1")
	client := &http.Client{Timeout: 45 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return errors.New("could not connect to bot.Box")
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusNoContent:
		fmt.Println("ChatGPT plan connected. Return to bot.Box and take control of the bot computer.")
		return nil
	case http.StatusConflict:
		return errors.New("the pairing expired or was already used; start a new pairing in bot.Box")
	case http.StatusForbidden:
		return errors.New("bot.Box could not confirm the account or plan permission")
	default:
		return fmt.Errorf("bot.Box could not complete the connection (HTTP %d)", resp.StatusCode)
	}
}

func controlPlaneURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" ||
		(u.Path != "" && u.Path != "/") ||
		(u.Scheme != "https" && (u.Scheme != "http" || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost"))) {
		return nil, errors.New("enter the HTTPS bot.Box URL, or a local HTTP development URL")
	}
	u.Path = "/api/codex-plan/pairing/complete"
	return u, nil
}

func readPairing(ctx context.Context, input *bufio.Reader) (string, error) {
	info, err := os.Stdin.Stat()
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeCharDevice != 0 {
		status := exec.CommandContext(ctx, "stty", "-g")
		status.Stdin = os.Stdin
		saved, err := status.Output()
		if err != nil {
			return "", errors.New("could not read terminal mode")
		}
		blind := exec.CommandContext(ctx, "stty", "-echo")
		blind.Stdin = os.Stdin
		if blind.Run() != nil {
			return "", errors.New("could not hide the pairing code")
		}
		defer func() {
			restore := exec.CommandContext(context.WithoutCancel(ctx), "stty", strings.TrimSpace(string(saved))) //nolint:gosec // stty restores its own captured settings
			restore.Stdin = os.Stdin
			_ = restore.Run()
			fmt.Println()
		}()
	}
	code, err := input.ReadString('\n')
	return strings.TrimSpace(code), err
}

func authorize(ctx context.Context, pair codexplan.Pairing) (codexplan.Grant, error) {
	state := base64.RawURLEncoding.EncodeToString(random())
	nonce := base64.RawURLEncoding.EncodeToString(random())
	verifier := base64.RawURLEncoding.EncodeToString(random())
	challenge := sha256.Sum256([]byte(verifier))
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp4", "127.0.0.1:0")
	if err != nil {
		return codexplan.Grant{}, errors.New("could not start the local OAuth callback")
	}
	host := listener.Addr().String()
	redirect := "http://" + host + codexplan.CallbackPath
	callback := make(chan codexplan.Grant, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+codexplan.CallbackPath, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		query := r.URL.Query()
		if r.Host != host || subtle.ConstantTimeCompare([]byte(query.Get("state")), []byte(state)) != 1 {
			http.Error(w, "This callback does not match the current sign-in.", http.StatusForbidden)
			return
		}
		id := query.Get("client_id")
		if id == "" {
			id = pair.ClientID
		}
		grant := codexplan.Grant{Code: query.Get("code"), ClientID: id, Verifier: verifier, Nonce: nonce, Redirect: redirect}
		if !grant.Valid() || query.Get("error") != "" || (pair.ClientID != codexplan.DynamicClient && id != pair.ClientID) {
			http.Error(w, "ChatGPT plan authorization failed. Return to the terminal.", http.StatusBadRequest)
			grant = codexplan.Grant{}
		} else {
			_, _ = fmt.Fprintln(w, "Authorization received. Finish the connection in your terminal.")
		}
		select {
		case callback <- grant:
		default:
		}
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second,
		MaxHeaderBytes: 16384, ErrorLog: log.New(os.Stderr, "Callback: ", 0)}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = server.Serve(listener)
	}()
	defer func() { _ = server.Close() }()
	address := codexplan.AuthorizationURL(pair.ClientID, pair.HostID(), redirect, state, nonce, base64.RawURLEncoding.EncodeToString(challenge[:]))
	browser := "xdg-open"
	if runtime.GOOS == "darwin" {
		browser = "open"
	}
	if err := exec.CommandContext(ctx, browser, address).Run(); err != nil { //nolint:gosec // fixed opener and verified OpenAI URL
		fmt.Printf("Open this one-time authorization link in your workstation browser:\n%s\n", address)
	}
	select {
	case <-ctx.Done():
		return codexplan.Grant{}, errors.New("ChatGPT authorization cancelled or timed out")
	case <-done:
		return codexplan.Grant{}, errors.New("local OAuth listener stopped")
	case grant := <-callback:
		if !grant.Valid() {
			return codexplan.Grant{}, errors.New("ChatGPT plan authorization was declined or failed")
		}
		return grant, nil
	}
}

func random() []byte {
	random := make([]byte, 32)
	_, _ = rand.Read(random)
	return random
}
