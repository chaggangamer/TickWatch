package alert

import (
	"fmt"
	"time"
)

type Message struct {
	Title string
	Body  string
}

func buildMessage(ev event) Message {
	name := ev.monitor.Name
	target := ev.monitor.Target()
	switch ev.kind {
	case KindDown:
		return Message{
			Title: fmt.Sprintf("Tickwatch: %s is DOWN", name),
			Body: fmt.Sprintf("%s %s is down.\nReason: %s\nStarted: %s",
				ev.monitor.Type, target, ev.reason, formatTime(time.Now().Unix())),
		}
	case KindRecovered:
		return Message{
			Title: fmt.Sprintf("Tickwatch: %s is UP again", name),
			Body: fmt.Sprintf("%s %s recovered after %s.\nRecovered: %s",
				ev.monitor.Type, target, formatDuration(ev.downtime), formatTime(time.Now().Unix())),
		}
	case KindReminder:
		return Message{
			Title: fmt.Sprintf("Tickwatch: %s is still DOWN", name),
			Body: fmt.Sprintf("%s %s has been down for %s.\nReason: %s",
				ev.monitor.Type, target, formatDuration(ev.downtime), ev.reason),
		}
	case KindSSL:
		return Message{
			Title: fmt.Sprintf("Tickwatch: SSL certificate expiring for %s", name),
			Body: fmt.Sprintf("The certificate for %s %s expires in %d days (%s).",
				ev.monitor.Type, target, ev.sslDays, formatTime(ev.sslExpiry)),
		}
	default:
		return Message{Title: "Tickwatch notification", Body: ""}
	}
}

func formatDuration(sec int64) string {
	if sec < 0 {
		sec = 0
	}
	d := sec / 86400
	h := (sec % 86400) / 3600
	m := (sec % 3600) / 60
	s := sec % 60
	switch {
	case d > 0:
		return fmt.Sprintf("%dd %dh %dm", d, h, m)
	case h > 0:
		return fmt.Sprintf("%dh %dm", h, m)
	case m > 0:
		return fmt.Sprintf("%dm %ds", m, s)
	default:
		return fmt.Sprintf("%ds", s)
	}
}

func formatTime(unix int64) string {
	return time.Unix(unix, 0).UTC().Format("2006-01-02 15:04:05 UTC")
}
