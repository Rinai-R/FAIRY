package visual

type VisualService struct {
	root string
}

func NewVisualService(root string) *VisualService {
	return &VisualService{root: root}
}

func (s *VisualService) ListVisualPacks() (Catalog, error) {
	return ListManifests(s.root)
}
