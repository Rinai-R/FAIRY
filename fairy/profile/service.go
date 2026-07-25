package profile

type ProfileService struct {
	root  string
	store *Store
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

func (s *ProfileService) Current() (*Snapshot, error) {
	return s.store.Current()
}

func (s *ProfileService) SetPreferredName(preferredName *string) (Update, error) {
	update, err := s.store.SetPreferredName(preferredName)
	if err != nil {
		return Update{}, err
	}
	return update, nil
}

func (s *ProfileService) Clear() (Update, error) {
	update, err := s.store.Clear()
	if err != nil {
		return Update{}, err
	}
	return update, nil
}
