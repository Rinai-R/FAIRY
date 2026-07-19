package coreclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
)

var configSections = map[string]struct{}{
	"model": {}, "speech": {}, "web-search": {}, "semantic-embedding": {},
}

func (c *Client) GetConfig(ctx context.Context, section string) (json.RawMessage, error) {
	if _, ok := configSections[section]; !ok {
		return nil, errors.New("unsupported config section")
	}
	return c.doRawJSON(ctx, "read "+section+" config", http.MethodGet, "/v1/config/"+section, nil)
}

func (c *Client) ApplyConfig(ctx context.Context, section string, payload []byte) (json.RawMessage, error) {
	if _, ok := configSections[section]; !ok {
		return nil, errors.New("unsupported config section")
	}
	if err := validateJSONObject(payload); err != nil {
		return nil, err
	}
	return c.doRawJSON(ctx, "apply "+section+" config", http.MethodPut, "/v1/config/"+section, payload)
}

func (c *Client) DeleteConfig(ctx context.Context, section string) (json.RawMessage, error) {
	if section != "model" && section != "speech" {
		return nil, errors.New("delete is supported only for model and speech")
	}
	return c.doRawJSON(ctx, "delete "+section+" config", http.MethodDelete, "/v1/config/"+section, nil)
}

func (c *Client) GetProfile(ctx context.Context) (json.RawMessage, error) {
	return c.doRawJSON(ctx, "read profile", http.MethodGet, "/v1/profile", nil)
}

func (c *Client) ApplyProfile(ctx context.Context, payload []byte) (json.RawMessage, error) {
	if err := validateJSONObject(payload); err != nil {
		return nil, err
	}
	return c.doRawJSON(ctx, "apply profile", http.MethodPut, "/v1/profile", payload)
}

func (c *Client) DeleteProfile(ctx context.Context) (json.RawMessage, error) {
	return c.doRawJSON(ctx, "delete profile", http.MethodDelete, "/v1/profile", nil)
}

func (c *Client) ListCharacters(ctx context.Context) (CharacterCatalog, error) {
	var result CharacterCatalog
	err := c.doJSON(ctx, "list characters", http.MethodGet, "/v1/characters", nil, &result)
	if err == nil && result.Characters == nil {
		err = errors.New("character response is missing characters")
	}
	return result, err
}

func (c *Client) CreateCharacter(ctx context.Context, payload []byte) (json.RawMessage, error) {
	if err := validateJSONObject(payload); err != nil {
		return nil, err
	}
	return c.doRawJSON(ctx, "create character", http.MethodPost, "/v1/characters", payload)
}

func (c *Client) ActivateCharacter(ctx context.Context, characterID string, revision uint64) (json.RawMessage, error) {
	body, _ := json.Marshal(map[string]uint64{"revision": revision})
	path := "/v1/characters/" + url.PathEscape(characterID) + "/activate"
	return c.doRawJSON(ctx, "activate character", http.MethodPost, path, body)
}
