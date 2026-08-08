package personal

import "context"

func (s *Store) PersonalMemoryCatalog(characterID string) (Catalog, error) {
	return s.PersonalMemoryCatalogContext(context.Background(), characterID)
}

func (s *Store) PersonalMemoryCatalogContext(ctx context.Context, characterID string) (Catalog, error) {
	return s.personalMemoryCatalogPostgres(ctx, characterID)
}

func (s *Store) CreatePersonalMemory(kind string, scope Scope, content string, confidence uint16) (Record, error) {
	return s.CreatePersonalMemoryContext(context.Background(), kind, scope, content, confidence)
}

func (s *Store) CreatePersonalMemoryContext(ctx context.Context, kind string, scope Scope, content string, confidence uint16) (Record, error) {
	return s.createPersonalMemoryPostgres(ctx, kind, scope, content, confidence)
}

func (s *Store) RevisePersonalMemory(id string, content string, confidence uint16) (Record, error) {
	return s.RevisePersonalMemoryContext(context.Background(), id, content, confidence)
}

func (s *Store) RevisePersonalMemoryContext(ctx context.Context, id string, content string, confidence uint16) (Record, error) {
	return s.revisePersonalMemoryPostgres(ctx, id, content, confidence)
}

func (s *Store) TombstonePersonalMemory(id string) error {
	return s.TombstonePersonalMemoryContext(context.Background(), id)
}

func (s *Store) TombstonePersonalMemoryContext(ctx context.Context, id string) error {
	return s.tombstonePersonalMemoryPostgres(ctx, id)
}

func (s *Store) AssignLegacyRelationship(id string, characterID string) (Record, error) {
	return s.AssignLegacyRelationshipContext(context.Background(), id, characterID)
}

func (s *Store) AssignLegacyRelationshipContext(ctx context.Context, id string, characterID string) (Record, error) {
	return s.assignLegacyRelationshipPostgres(ctx, id, characterID)
}
