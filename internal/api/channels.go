package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/chaggangamer/tickwatch/internal/alert"
	"github.com/chaggangamer/tickwatch/internal/store"
)

var validChannelTypes = map[string]bool{
	"telegram": true, "slack": true, "discord": true, "smtp": true, "webhook": true,
}

type channelRequest struct {
	Type   string          `json:"type"`
	Name   string          `json:"name"`
	Config json.RawMessage `json:"config"`
}

type channelJSON struct {
	ID        int64          `json:"id"`
	Type      string         `json:"type"`
	Name      string         `json:"name"`
	Config    map[string]any `json:"config"`
	CreatedAt int64          `json:"created_at"`
}

func channelOut(c store.Channel) channelJSON {
	return channelJSON{
		ID:        c.ID,
		Type:      c.Type,
		Name:      c.Name,
		Config:    alert.MaskConfig(c.Type, c.Config),
		CreatedAt: c.CreatedAt,
	}
}

func validateChannel(req *channelRequest) error {
	if len(req.Name) < 1 || len(req.Name) > 120 {
		return errors.New("name must be between 1 and 120 characters")
	}
	if !validChannelTypes[req.Type] {
		return errors.New("unknown channel type")
	}
	var cfg map[string]any
	if err := json.Unmarshal(req.Config, &cfg); err != nil || cfg == nil {
		return errors.New("config must be a json object")
	}
	return nil
}

func listChannels(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		channels, err := d.Store.Channels()
		if err != nil {
			writeErr(w, 500, "internal", "list channels failed")
			return
		}
		out := make([]channelJSON, 0, len(channels))
		for _, c := range channels {
			out = append(out, channelOut(c))
		}
		writeJSON(w, 200, map[string]any{"items": out})
	}
}

func createChannel(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req channelRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, 400, "invalid_json", "request body is not valid json")
			return
		}
		if err := validateChannel(&req); err != nil {
			writeErr(w, 400, "invalid_channel", err.Error())
			return
		}
		c := &store.Channel{
			Type:      req.Type,
			Name:      req.Name,
			Config:    req.Config,
			CreatedAt: time.Now().Unix(),
		}
		if err := d.Store.CreateChannel(c); err != nil {
			writeErr(w, 500, "internal", "create channel failed")
			return
		}
		writeJSON(w, 201, channelOut(*c))
	}
}

func getChannel(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(r)
		if !ok {
			writeErr(w, 400, "invalid_id", "channel id must be a positive integer")
			return
		}
		c, err := d.Store.Channel(id)
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, 404, "not_found", "channel not found")
			return
		}
		if err != nil {
			writeErr(w, 500, "internal", "get channel failed")
			return
		}
		writeJSON(w, 200, channelOut(*c))
	}
}

func updateChannel(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(r)
		if !ok {
			writeErr(w, 400, "invalid_id", "channel id must be a positive integer")
			return
		}
		existing, err := d.Store.Channel(id)
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, 404, "not_found", "channel not found")
			return
		}
		if err != nil {
			writeErr(w, 500, "internal", "get channel failed")
			return
		}
		var req channelRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, 400, "invalid_json", "request body is not valid json")
			return
		}
		if err := validateChannel(&req); err != nil {
			writeErr(w, 400, "invalid_channel", err.Error())
			return
		}
		merged, err := alert.MergeMaskedConfig(req.Type, existing.Config, req.Config)
		if err != nil {
			writeErr(w, 400, "invalid_channel", err.Error())
			return
		}
		existing.Type = req.Type
		existing.Name = req.Name
		existing.Config = merged
		if err := d.Store.UpdateChannel(existing); err != nil {
			writeErr(w, 500, "internal", "update channel failed")
			return
		}
		writeJSON(w, 200, channelOut(*existing))
	}
}

func deleteChannel(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(r)
		if !ok {
			writeErr(w, 400, "invalid_id", "channel id must be a positive integer")
			return
		}
		err := d.Store.DeleteChannel(id)
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, 404, "not_found", "channel not found")
			return
		}
		if err != nil {
			writeErr(w, 500, "internal", "delete channel failed")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func testChannel(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(r)
		if !ok {
			writeErr(w, 400, "invalid_id", "channel id must be a positive integer")
			return
		}
		if _, err := d.Store.Channel(id); errors.Is(err, store.ErrNotFound) {
			writeErr(w, 404, "not_found", "channel not found")
			return
		}
		if err := d.Alerts.SendTest(id); err != nil {
			writeErr(w, 502, "send_failed", err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true})
	}
}
