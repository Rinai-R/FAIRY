package profile

import "fairy/notify"

type ProfileService struct {
	root string
}

func NewProfileService(root string) *ProfileService {
	return &ProfileService{root: root}
}

func (s *ProfileService) Current() (*Snapshot, error) {
	return NewStore(s.root).Current()
}

func (s *ProfileService) SetPreferredName(preferredName *string) (Update, error) {
	update, err := NewStore(s.root).SetPreferredName(preferredName)
	if err != nil {
		return Update{}, err
	}
	var revision *uint64
	if update.Profile != nil {
		value := update.Profile.Revision
		revision = &value
	}
	notify.Emit(notify.UserProfileChanged(revision))
	return update, nil
}

func (s *ProfileService) Clear() (Update, error) {
	update, err := NewStore(s.root).Clear()
	if err != nil {
		return Update{}, err
	}
	notify.Emit(notify.UserProfileChanged(nil))
	return update, nil
}
