package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/nanaki-93/kogin/v2/internal/launcher"
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/services/notifications"
)

// notifyOutcomes follows the daemon's events: it keeps the remembered project
// current when the active project changes in the Web UI, and sends a system
// notification per finished task or analysis run while the window is not in
// front. Without notifications (a program outside an app bundle, or the
// permission refused) project tracking still runs.
func (s *shell) notifyOutcomes(session *launcher.Session) {
	ctx := s.app.Context()
	service := s.startNotifications(ctx, session)
	notices := launcher.NewNotices()
	session.Client.Watch(ctx, func(event launcher.Event) {
		if event.Kind == "project_index" {
			if current := session.Client.CurrentProject(ctx); current != "" {
				_ = launcher.RememberProject(current)
			}
			return
		}
		if service == nil {
			return
		}
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

func (s *shell) startNotifications(ctx context.Context, session *launcher.Session) *notifications.NotificationService {
	// Started here rather than as an app service: a service that fails to
	// start stops the whole app.
	service := notifications.New()
	if err := service.ServiceStartup(ctx, application.ServiceOptions{}); err != nil {
		s.note("Notifications are off: %v", err)
		return nil
	}
	s.mu.Lock()
	s.notifications = service
	s.mu.Unlock()
	if granted, err := service.RequestNotificationAuthorization(); err != nil || !granted {
		s.note("Notifications are off: the system did not allow them (%v). Allow Kogin in System Settings → Notifications and restart it.", err)
		return nil
	}
	service.OnNotificationResponse(func(result notifications.NotificationResult) {
		if result.Error != nil || strings.Contains(result.Response.ActionIdentifier, "Dismiss") {
			return
		}
		route, _ := result.Response.UserInfo["route"].(string)
		s.openPage(session, route)
	})
	return service
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
		fmt.Fprintf(s.log, "Kogin: "+format+"\n", args...)
	}
}
