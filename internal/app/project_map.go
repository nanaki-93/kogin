package app

import (
	"sync"

	"github.com/nanaki-93/kasuri/v2/internal/project"
	"github.com/nanaki-93/kasuri/v2/internal/project/projectmap"
)

// projectMapCache keeps the project map of the latest indexed revision: it
// reads files to find entry points and manifests, so it is built once per
// revision.
type projectMapCache struct {
	mu             sync.Mutex
	root, revision string
	built          projectmap.Map
}

// projectMap is the map of the indexed project.
func (s *Service) projectMap(index *project.ProjectIndex) projectmap.Map {
	root := s.manager.Root()
	cache := &s.projectMaps
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.root != root || cache.revision != index.ProjectRevision || index.ProjectRevision == "" {
		cache.built = projectmap.Build(root, index.Files)
		cache.root, cache.revision = root, index.ProjectRevision
	}
	return cache.built
}
