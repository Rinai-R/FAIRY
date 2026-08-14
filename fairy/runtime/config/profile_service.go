package config

import "errors"

type ProfileService struct {
	root  string
	store *ProfileStore
}

func NewProfileService(root string) *ProfileService {
	return &ProfileService{root: root, store: NewProfileStore(root)}
}

// NewProfileServiceWithStore wires the service to one authoritative profile store.
func NewProfileServiceWithStore(store *ProfileStore) (*ProfileService, error) {
	if store == nil {
		return nil, errors.New("profile store is required")
	}
	return &ProfileService{store: store}, nil
}

// ProfileStore returns the process-scoped user-profile store for sharing with
// other composition-root consumers (e.g. companion).
func (s *ProfileService) ProfileStore() *ProfileStore {
	if s == nil {
		return nil
	}
	return s.store
}

func (s *ProfileService) Current() (*ProfileSnapshot, error) {
	return s.store.Current()
}

func (s *ProfileService) SetPreferredName(preferredName *string) (ProfileUpdate, error) {
	update, err := s.store.SetPreferredName(preferredName)
	if err != nil {
		return ProfileUpdate{}, err
	}
	return update, nil
}

func (s *ProfileService) Clear() (ProfileUpdate, error) {
	update, err := s.store.Clear()
	if err != nil {
		return ProfileUpdate{}, err
	}
	return update, nil
}
