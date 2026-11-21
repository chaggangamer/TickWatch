package alert

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/chaggangamer/tickwatch/internal/store"
)

const (
	KindDown      = "down"
	KindRecovered = "recovered"
	KindReminder  = "reminder"
	KindSSL       = "ssl"
	KindTest      = "test"
)

type event struct {
	monitor    store.Monitor
	kind       string
	reason     string
	incidentID int64
	downtime   int64
	sslDays    int
	sslExpiry  int64
}

type Dispatcher struct {
	store         store.Store
	log           *slog.Logger
	events        chan event
	client        *http.Client
	reminderEvery time.Duration
	telegramAPI   string
}

func New(s store.Store, log *slog.Logger) *Dispatcher {
	if log == nil {
		log = slog.Default()
	}
	return &Dispatcher{
		store:         s,
		log:           log,
		events:        make(chan event, 64),
		client:        &http.Client{Timeout: 10 * time.Second},
		reminderEvery: 30 * time.Second,
		telegramAPI:   "https://api.telegram.org",
	}
}

func (d *Dispatcher) Start(ctx context.Context) {
	go d.loop(ctx)
}

func (d *Dispatcher) loop(ctx context.Context) {
	ticker := time.NewTicker(d.reminderEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			d.drain()
			return
		case ev := <-d.events:
			d.deliver(ev)
		case <-ticker.C:
			d.sendReminders()
		}
	}
}

func (d *Dispatcher) drain() {
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case ev := <-d.events:
			d.deliver(ev)
		default:
			return
		}
	}
}

func (d *Dispatcher) NotifyDown(m store.Monitor, reason string, incidentID int64) {
	d.submit(event{monitor: m, kind: KindDown, reason: reason, incidentID: incidentID})
}

func (d *Dispatcher) NotifyRecovered(m store.Monitor, downtime int64) {
	d.submit(event{monitor: m, kind: KindRecovered, downtime: downtime})
}

func (d *Dispatcher) NotifySSL(m store.Monitor, days int, expiry int64) {
	d.submit(event{monitor: m, kind: KindSSL, sslDays: days, sslExpiry: expiry})
}

func (d *Dispatcher) submit(ev event) {
	select {
	case d.events <- ev:
	default:
		d.log.Warn("alert queue full, dropping event", "monitor_id", ev.monitor.ID, "kind", ev.kind)
	}
}

func (d *Dispatcher) deliver(ev event) {
	channels, err := d.store.MonitorChannels(ev.monitor.ID)
	if err != nil {
		d.log.Error("load monitor channels failed", "monitor_id", ev.monitor.ID, "err", err)
		return
	}
	if len(channels) == 0 {
		return
	}
	now := time.Now().Unix()
	for _, ch := range channels {
		if ev.kind == KindDown || ev.kind == KindReminder {
			last, lastIncident, ok, err := d.store.LastAlert(ev.monitor.ID, ch.ID)
			if err != nil {
				d.log.Error("load alert state failed", "channel_id", ch.ID, "err", err)
			} else if ok && lastIncident == ev.incidentID && now-last < int64(ev.monitor.Cooldown) {
				continue
			}
		}
		msg := buildMessage(ev)
		if err := d.send(ch, msg, ev); err != nil {
			d.log.Warn("alert send failed",
				"monitor_id", ev.monitor.ID, "channel_id", ch.ID, "channel_type", ch.Type, "err", err)
			continue
		}
		if ev.kind == KindDown || ev.kind == KindReminder {
			if err := d.store.SaveAlert(ev.monitor.ID, ch.ID, now, ev.incidentID); err != nil {
				d.log.Error("save alert state failed", "channel_id", ch.ID, "err", err)
			}
		}
	}
}

func (d *Dispatcher) sendReminders() {
	monitors, err := d.store.DownMonitors()
	if err != nil {
		d.log.Error("list down monitors failed", "err", err)
		return
	}
	if len(monitors) == 0 {
		return
	}
	open, err := d.store.OpenIncidents()
	if err != nil {
		d.log.Error("list open incidents failed", "err", err)
		return
	}
	incidentOf := map[int64]store.Incident{}
	for _, inc := range open {
		incidentOf[inc.MonitorID] = inc
	}
	now := time.Now().Unix()
	for _, m := range monitors {
		inc, has := incidentOf[m.ID]
		if !has {
			continue
		}
		d.deliver(event{
			monitor:    m,
			kind:       KindReminder,
			reason:     inc.Reason,
			incidentID: inc.ID,
			downtime:   now - inc.StartedAt,
		})
	}
}

func (d *Dispatcher) send(ch store.Channel, msg Message, ev event) error {
	switch ch.Type {
	case "telegram":
		return d.sendTelegram(ch, msg)
	case "slack":
		return d.sendSlack(ch, msg)
	case "discord":
		return d.sendDiscord(ch, msg)
	case "smtp":
		return d.sendSMTP(ch, msg)
	case "webhook":
		return d.sendWebhook(ch, msg, ev)
	default:
		return fmt.Errorf("unknown channel type %q", ch.Type)
	}
}

func (d *Dispatcher) SendTest(channelID int64) error {
	ch, err := d.store.Channel(channelID)
	if err != nil {
		return err
	}
	msg := Message{
		Title: "Tickwatch test notification",
		Body:  fmt.Sprintf("This is a test from Tickwatch for the %s channel %q. If you can read this, it works.", ch.Type, ch.Name),
	}
	return d.send(*ch, msg, event{kind: KindTest})
}
