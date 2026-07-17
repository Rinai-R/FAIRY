package profile

import "fairy/notify"

type ProfileService struct {
	root  string
	store *Store
	emit  notify.ConfigEmitter
}

func NewProfileService(root string) *ProfileService {
	return &ProfileService{root: root, store: NewStore(root)}
}

// ProfileStore returns the process-scoped user-profile store for sharing with
// other composition-root consumers (e.g. companion).
func (s *ProfileService) ProfileStore() *Store {
	if s == nil {
		return nil
	}
	return s.store
}

// AttachConfigEmitter wires configuration-change delivery from main.
func AttachConfigEmitter(s *ProfileService, emit notify.ConfigEmitter) {
	if s == nil {
		return
	}
	s.emit = emit
}

func (s *ProfileService) emitChange(change notify.ConfigurationChange) {
	if s != nil && s.emit != nil {
		s.emit(change)
	}
}

func (s *ProfileService) Current() (*Snapshot, error) {
	return s.store.Current()
}

func (s *ProfileService) SetPreferredName(preferredName *string) (Update, error) {
	update, err := s.store.SetPreferredName(preferredName)
	if err != nil {
		return Update{}, err
	}
	var revision *uint64
	if update.Profile != nil {
		value := update.Profile.Revision
		revision = &value
	}
	s.emitChange(notify.UserProfileChanged(revision))
	return update, nil
}

func (s *ProfileService) Clear() (Update, error) {
	update, err := s.store.Clear()
	if err != nil {
		return Update{}, err
	}
	s.emitChange(notify.UserProfileChanged(nil))
	return update, nil
}
