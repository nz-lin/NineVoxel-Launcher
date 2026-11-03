package loader

import (
	"context"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// LoaderService exposes read-only loader catalog discovery to the frontend.
// Installation mutations remain on InstanceService so loader state and the
// instance manifest are updated atomically.
type LoaderService struct{}

func NewWailsService() application.Service {
	return application.NewService(&LoaderService{})
}

// ListVersions returns official loader releases compatible with one Minecraft version.
func (s *LoaderService) ListVersions(req VersionListRequest) (VersionList, error) {
	result, appErr := ListVersions(context.Background(), req)
	if appErr != nil {
		return VersionList{}, appErr
	}
	return result, nil
}
