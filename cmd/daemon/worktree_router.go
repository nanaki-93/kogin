package main

import (
	"net/http"
	"sync"

	"github.com/nanaki-93/kasuri/v2/internal/app"
	"github.com/nanaki-93/kasuri/v2/internal/project"
)

// Cache recent checkouts with a bounded cap. Registration and scope are still
// checked by WorktreeService on every request, and old services cannot accumulate.
const maxCachedWorktreeRouters = 32

type worktreeRouteEntry struct {
	service *app.Service
	routes  http.Handler
}

type worktreeRouter struct {
	mu      sync.Mutex
	entries []worktreeRouteEntry
	token   string
}

func (router *worktreeRouter) handler(service *app.Service, manager *project.Manager) http.Handler {
	router.mu.Lock()
	defer router.mu.Unlock()
	for i := range router.entries {
		if router.entries[i].service == service {
			return router.entries[i].routes
		}
	}
	routes := newProjectHTTPMux(service, manager, router.token)
	if len(router.entries) >= maxCachedWorktreeRouters {
		copy(router.entries, router.entries[1:])
		router.entries[len(router.entries)-1] = worktreeRouteEntry{service: service, routes: routes}
	} else {
		router.entries = append(router.entries, worktreeRouteEntry{service: service, routes: routes})
	}
	return routes
}
