package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/teslashibe/agent-go/internal/bridge"
	"github.com/teslashibe/agent-go/internal/config"
	"github.com/teslashibe/imessage"
)

func TestSenderExactTapback(t *testing.T) {
	for _, transport := range []string{"local", "ssh"} {
		for _, group := range []bool{false, true} {
			for _, reaction := range []string{"love", "like", "dislike", "laugh", "emphasize", "question"} {
				t.Run(transport+"/"+reaction+map[bool]string{false: "/dm", true: "/group"}[group], func(t *testing.T) {
					clientSide, peer := net.Pipe()
					client := imessage.NewClient(clientSide, clientSide)
					defer client.Close()
					defer peer.Close()
					peer.SetDeadline(time.Now().Add(5 * time.Second))
					cfg := config.Config{ChatID: 42, Transport: transport, Group: group}
					s := &sender{client: client, chats: map[int64]config.Config{42: cfg, 99: {ChatID: 99}}}
					done := make(chan error, 1)
					go func() {
						accepted, err := s.React(context.Background(), 42, "older-authenticated-job-guid", reaction)
						if err == nil && !accepted.Accepted {
							err = errors.New("acceptance lost")
						}
						done <- err
					}()
					var req struct {
						ID     string `json:"id"`
						Method string `json:"method"`
						Params struct {
							ChatID   int64  `json:"chat_id"`
							GUID     string `json:"message_guid"`
							Reaction string `json:"reaction"`
							Remove   bool   `json:"remove"`
						} `json:"params"`
					}
					if err := json.NewDecoder(peer).Decode(&req); err != nil {
						t.Fatal(err)
					}
					if req.Method != "tapback" || req.Params.ChatID != 42 || req.Params.GUID != "older-authenticated-job-guid" || req.Params.Reaction != reaction || req.Params.Remove {
						t.Fatalf("wrong target or history-window request: %+v", req)
					}
					if err := json.NewEncoder(peer).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"ok": true, "reaction": reaction}}); err != nil {
						t.Fatal(err)
					}
					if err := <-done; err != nil {
						t.Fatal(err)
					}
				})
			}
		}
	}
}

func TestSenderTapbackFailureHasNoFallback(t *testing.T) {
	for _, result := range []map[string]any{{"error": map[string]any{"code": -32601, "message": "tapback capability unavailable"}}, {"result": map[string]any{"ok": false, "reaction": "like"}}, {"result": map[string]any{"ok": true, "reaction": "love"}}} {
		clientSide, peer := net.Pipe()
		client := imessage.NewClient(clientSide, clientSide)
		peer.SetDeadline(time.Now().Add(5 * time.Second))
		s := &sender{client: client, cfg: config.Config{ChatID: 42, Transport: "local"}}
		done := make(chan error, 1)
		go func() {
			accepted, err := s.React(context.Background(), 42, "guid", "like")
			if accepted.Accepted || err == nil {
				done <- errors.New("failure reported as success")
				return
			}
			if result["error"] != nil {
				var rpc *imessage.RPCError
				if !errors.As(err, &rpc) {
					done <- errors.New("capability error lost")
					return
				}
			}
			done <- nil
		}()
		var req struct {
			ID string `json:"id"`
		}
		if err := json.NewDecoder(peer).Decode(&req); err != nil {
			t.Fatal(err)
		}
		result["id"] = req.ID
		result["jsonrpc"] = "2.0"
		if err := json.NewEncoder(peer).Encode(result); err != nil {
			t.Fatal(err)
		}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		client.Close()
		peer.Close()
	}
}

func TestSenderTapbackRejectsBeforeTransport(t *testing.T) {
	s := &sender{cfg: config.Config{ChatID: 42}}
	if accepted, err := s.React(context.Background(), 99, "guid", "like"); accepted.Accepted || !errors.Is(err, errIdentity) {
		t.Fatal(accepted, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if accepted, err := s.React(ctx, 42, "guid", "like"); accepted.Accepted || !errors.Is(err, context.Canceled) {
		t.Fatal(accepted, err)
	}
}

func TestSenderPreservesNativeVerification(t *testing.T) {
	clientSide, peer := net.Pipe()
	client := imessage.NewClient(clientSide, clientSide)
	defer client.Close()
	defer peer.Close()
	peer.SetDeadline(time.Now().Add(5 * time.Second))
	s := &sender{client: client, cfg: config.Config{ChatID: 42}}
	done := make(chan bridge.ReactionResult, 1)
	go func() {
		result, err := s.React(context.Background(), 42, "exact-guid", "love")
		if err != nil {
			done <- bridge.ReactionResult{}
			return
		}
		done <- result
	}()
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(peer).Decode(&req); err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(peer).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"ok": true, "reaction": "love", "verified": true}}); err != nil {
		t.Fatal(err)
	}
	got := <-done
	if !got.Accepted || !got.Verified {
		t.Fatal("native evidence lost", got)
	}
}
