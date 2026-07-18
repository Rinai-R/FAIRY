package speech

import (
	"context"
	"fmt"

	"fairy/secret"
)

type VoiceCloneClient interface {
	TrainVoice(ctx context.Context, settings Settings, credentials Credentials, request TrainVoiceRequest) (VoiceResult, error)
	QueryVoice(ctx context.Context, settings Settings, credentials Credentials, request VoiceOperationRequest) (VoiceResult, error)
	UpgradeVoice(ctx context.Context, settings Settings, credentials Credentials, request VoiceOperationRequest) (VoiceResult, error)
}

type SpeechService struct {
	root    string
	secrets *secret.Store
	client  VoiceCloneClient
}

func NewSpeechService(root string, secrets *secret.Store) *SpeechService {
	return NewSpeechServiceWithClient(root, secrets, NewClient())
}

func NewSpeechServiceWithClient(root string, secrets *secret.Store, client VoiceCloneClient) *SpeechService {
	if client == nil {
		client = NewClient()
	}
	return &SpeechService{root: root, secrets: secrets, client: client}
}

func (s *SpeechService) Status() (Status, error) {
	if s == nil {
		return Status{}, ErrConfigRootRequired
	}
	return ReadStatus(s.root, s.secrets)
}

func (s *SpeechService) SaveSettings(input SaveSettingsRequest) (Status, error) {
	if s == nil {
		return Status{}, ErrConfigRootRequired
	}
	return SaveSettings(s.root, input, s.secrets)
}

func (s *SpeechService) ClearSettings() (Status, error) {
	if s == nil {
		return Status{}, ErrConfigRootRequired
	}
	return ClearSettings(s.root, s.secrets)
}

func (s *SpeechService) TrainVoice(request TrainVoiceRequest) (VoiceResult, error) {
	settings, credentials, err := s.readySettings()
	if err != nil {
		return VoiceResult{}, err
	}
	request = normalizeTrainRequest(settings, request)
	if err := validateTrainRequest(request); err != nil {
		return VoiceResult{}, err
	}
	result, err := s.client.TrainVoice(context.Background(), settings, credentials, request)
	if err != nil {
		return VoiceResult{}, fmt.Errorf("training volcengine voice clone: %w", err)
	}
	return result, nil
}

func (s *SpeechService) QueryVoice(request VoiceOperationRequest) (VoiceResult, error) {
	settings, credentials, err := s.readySettings()
	if err != nil {
		return VoiceResult{}, err
	}
	request.SpeakerID = defaultString(request.SpeakerID, settings.DefaultSpeaker)
	if request.SpeakerID == "" {
		return VoiceResult{}, ErrSpeakerIDRequired
	}
	result, err := s.client.QueryVoice(context.Background(), settings, credentials, request)
	if err != nil {
		return VoiceResult{}, fmt.Errorf("querying volcengine voice clone: %w", err)
	}
	return result, nil
}

func (s *SpeechService) UpgradeVoice(request VoiceOperationRequest) (VoiceResult, error) {
	settings, credentials, err := s.readySettings()
	if err != nil {
		return VoiceResult{}, err
	}
	request.SpeakerID = defaultString(request.SpeakerID, settings.DefaultSpeaker)
	if request.SpeakerID == "" {
		return VoiceResult{}, ErrSpeakerIDRequired
	}
	result, err := s.client.UpgradeVoice(context.Background(), settings, credentials, request)
	if err != nil {
		return VoiceResult{}, fmt.Errorf("upgrading volcengine voice clone: %w", err)
	}
	return result, nil
}

func (s *SpeechService) readySettings() (Settings, Credentials, error) {
	if s == nil {
		return Settings{}, Credentials{}, ErrConfigRootRequired
	}
	return loadReadySettings(s.root, s.secrets)
}
