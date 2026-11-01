package main

import (
	"fmt"
	"strings"

	"github.com/nanaki-93/kasuri/v2/internal/launcher"
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/services/notifications"
)

// notifyOutcomes tells what agents and analysis finished while the window was
// not in front: a system notification per outcome, which brings the window
// forward on that page when clicked. Without notifications (a program outside
// an app bundle, or the permission refused) the app works as before.
func (s *shell) notifyOutcomes(session *launcher.Session) {
	ctx := s.app.Context()
	// Started here rather than as an app service: a service that fails to
	// start stops the whole app.
	service := notifications.New()
	if err := service.ServiceStartup(ctx, application.ServiceOptions{}); err != nil {
		s.note("Notifications are off: %v", err)
		return
	}
	s.mu.Lock()
	s.notifications = service
	s.mu.Unlock()
	if granted, err := service.RequestNotificationAuthorization(); err != nil || !granted {
		s.note("Notifications are off: the system did not allow them (%v). Allow Kasuri in System Settings → Notifications and restart it.", err)
		return
	}
	service.OnNotificationResponse(func(result notifications.NotificationResult) {
		if result.Error != nil || strings.Contains(result.Response.ActionIdentifier, "Dismiss") {
			return
		}
		route, _ := result.Response.UserInfo["route"].(string)
		s.openPage(session, route)
	})
	notices := launcher.NewNotices()
	session.Client.Watch(ctx, func(event launcher.Event) {
		// Every event goes through notices, so it keeps track while the
		// window is in front too; the Web UI announces those itself.
		notice, ok := notices.Notice(event)
		if !ok || s.window.IsFocused() {
			return
		}
		err := service.SendNotification(notifications.NotificationOptions{
			ID:       notice.ID,
			Title:    notice.Title,
			Body:     notice.Body,
			ThreadID: strings.SplitN(notice.ID, "-", 2)[0],
			Data:     map[string]any{"route": notice.Route},
		})
		if err != nil {
			s.note("A notification was not shown: %v", err)
		}
	})
}

// openPage brings the window forward on a page of the Web UI.
func (s *shell) openPage(session *launcher.Session, route string) {
	s.show()
	if route != "" {
		s.window.SetURL(session.Page() + "#" + route)
	}
}

// note records what the app itself did in the launch's log, beside the
// daemon's output.
func (s *shell) note(format string, args ...any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.log != nil {
		fmt.Fprintf(s.log, "Kasuri: "+format+"\n", args...)
	}
}
