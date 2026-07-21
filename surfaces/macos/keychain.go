package main

import (
	"errors"

	keychain "github.com/keybase/go-keychain"
)

const (
	keychainService = "com.rinai.fairy.macos"
	keychainAccount = "core-api-token"
)

var ErrTokenNotFound = errors.New("Core token is not present in macOS Keychain")

type TokenStore interface {
	Get() (string, error)
	Set(string) error
	Delete() error
}

type systemTokenStore struct {
	service string
	account string
}

func (s systemTokenStore) identifiers() (string, string) {
	service, account := s.service, s.account
	if service == "" {
		service = keychainService
	}
	if account == "" {
		account = keychainAccount
	}
	return service, account
}

func (s systemTokenStore) Get() (string, error) {
	service, account := s.identifiers()
	query := keychain.NewItem()
	query.SetSecClass(keychain.SecClassGenericPassword)
	query.SetService(service)
	query.SetAccount(account)
	query.SetMatchLimit(keychain.MatchLimitOne)
	query.SetReturnData(true)
	results, err := keychain.QueryItem(query)
	if err != nil {
		return "", err
	}
	if len(results) != 1 || len(results[0].Data) == 0 {
		return "", ErrTokenNotFound
	}
	return string(results[0].Data), nil
}

func (s systemTokenStore) Set(token string) error {
	service, account := s.identifiers()
	item := keychain.NewItem()
	item.SetSecClass(keychain.SecClassGenericPassword)
	item.SetService(service)
	item.SetAccount(account)
	item.SetLabel("FAIRY macOS Core API token")
	item.SetData([]byte(token))
	item.SetAccessible(keychain.AccessibleAfterFirstUnlock)
	_ = keychain.DeleteItem(item)
	return keychain.AddItem(item)
}

func (s systemTokenStore) Delete() error {
	service, account := s.identifiers()
	item := keychain.NewItem()
	item.SetSecClass(keychain.SecClassGenericPassword)
	item.SetService(service)
	item.SetAccount(account)
	return keychain.DeleteItem(item)
}
