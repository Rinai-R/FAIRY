package speech

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"

	"fairy/secret"
)

type fakeVoiceCloneClient struct {
	settings     Settings
	credentials  Credentials
	train        TrainVoiceRequest
	operation    VoiceOperationRequest
	result       VoiceResult
	err          error
	trainCalls   int
	queryCalls   int
	upgradeCalls int
}

func (f *fakeVoiceCloneClient) TrainVoice(_ context.Context, settings Settings, credentials Credentials, request TrainVoiceRequest) (VoiceResult, error) {
	f.trainCalls++
	f.settings = settings
	f.credentials = credentials
	f.train = request
	if f.err != nil {
		return VoiceResult{}, f.err
	}
	return f.result, nil
}

func (f *fakeVoiceCloneClient) QueryVoice(_ context.Context, settings Settings, credentials Credentials, request VoiceOperationRequest) (VoiceResult, error) {
	f.queryCalls++
	f.settings = settings
	f.credentials = credentials
	f.operation = request
	if f.err != nil {
		return VoiceResult{}, f.err
	}
	return f.result, nil
}

func (f *fakeVoiceCloneClient) UpgradeVoice(_ context.Context, settings Settings, credentials Credentials, request VoiceOperationRequest) (VoiceResult, error) {
	f.upgradeCalls++
	f.settings = settings
	f.credentials = credentials
	f.operation = request
	if f.err != nil {
		return VoiceResult{}, f.err
	}
	return f.result, nil
}

func TestSpeechServiceTrainVoiceUsesStoredSecretAndDefaults(t *testing.T) {
	root := t.TempDir()
	_, err := SaveSettings(root, SaveSettingsRequest{
		Enabled:        true,
		APIKey:         "test-api-key",
		DefaultSpeaker: "S_default",
		DefaultFormat:  "wav",
	}, nil)
	if err != nil {
		t.Fatalf("SaveSettings() error = %v", err)
	}
	fake := &fakeVoiceCloneClient{result: VoiceResult{SpeakerID: "S_default", Status: 2}}
	service := NewSpeechServiceWithClient(root, nil, fake)
	audio := base64.StdEncoding.EncodeToString([]byte("fake wav"))
	result, err := service.TrainVoice(TrainVoiceRequest{AudioData: audio})
	if err != nil {
		t.Fatalf("TrainVoice() error = %v", err)
	}
	if fake.trainCalls != 1 || fake.credentials.APIKey.Expose() != "test-api-key" || !fake.credentials.HasAPIKey {
		t.Fatalf("fake = %#v", fake)
	}
	if fake.train.SpeakerID != "S_default" || fake.train.AudioFormat != "wav" || fake.train.AudioData != audio {
		t.Fatalf("train request = %#v", fake.train)
	}
	if result.SpeakerID != "S_default" || result.Status != 2 {
		t.Fatalf("result = %#v", result)
	}
}

func TestSpeechServiceRejectsDisabledBeforeProvider(t *testing.T) {
	root := t.TempDir()
	fake := &fakeVoiceCloneClient{result: VoiceResult{SpeakerID: "S_voice"}}
	service := NewSpeechServiceWithClient(root, nil, fake)
	_, err := service.QueryVoice(VoiceOperationRequest{SpeakerID: "S_voice"})
	if err == nil {
		t.Fatal("QueryVoice() error = nil, want disabled")
	}
	if fake.queryCalls != 0 {
		t.Fatalf("provider calls = %d, want 0", fake.queryCalls)
	}
	if !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("error = %q, want disabled", err.Error())
	}
}

func TestSpeechServiceRejectsMissingTrainInputBeforeProvider(t *testing.T) {
	root := t.TempDir()
	_, err := SaveSettings(root, SaveSettingsRequest{Enabled: true, APIKey: "test-api-key"}, nil)
	if err != nil {
		t.Fatalf("SaveSettings() error = %v", err)
	}
	fake := &fakeVoiceCloneClient{}
	service := NewSpeechServiceWithClient(root, nil, fake)
	_, err = service.TrainVoice(TrainVoiceRequest{SpeakerID: "S_voice", AudioFormat: "wav"})
	if err == nil {
		t.Fatal("TrainVoice() error = nil, want audio error")
	}
	if fake.trainCalls != 0 {
		t.Fatalf("provider calls = %d, want 0", fake.trainCalls)
	}
}

func TestSpeechServiceQueryAndUpgradeUseDefaultSpeaker(t *testing.T) {
	root := t.TempDir()
	dbPath, err := secret.DatabasePath(root)
	if err != nil {
		t.Fatalf("DatabasePath() error = %v", err)
	}
	store := secret.NewStore(dbPath)
	value, err := secret.NewValue("access-token")
	if err != nil {
		t.Fatalf("NewValue() error = %v", err)
	}
	if err := store.Save(accessTokenSecretID, value); err != nil {
		t.Fatalf("Save(access token) error = %v", err)
	}
	_, err = SaveSettings(root, SaveSettingsRequest{Enabled: true, AppID: "appid", DefaultSpeaker: "S_default"}, store)
	if err != nil {
		t.Fatalf("SaveSettings() error = %v", err)
	}
	fake := &fakeVoiceCloneClient{result: VoiceResult{SpeakerID: "S_default", Status: 2}}
	service := NewSpeechServiceWithClient(root, store, fake)
	if _, err := service.QueryVoice(VoiceOperationRequest{}); err != nil {
		t.Fatalf("QueryVoice() error = %v", err)
	}
	if _, err := service.UpgradeVoice(VoiceOperationRequest{}); err != nil {
		t.Fatalf("UpgradeVoice() error = %v", err)
	}
	if fake.queryCalls != 1 || fake.upgradeCalls != 1 || fake.operation.SpeakerID != "S_default" {
		t.Fatalf("fake = %#v", fake)
	}
	if !fake.credentials.HasAccessToken || fake.credentials.AccessToken.Expose() != "access-token" {
		t.Fatalf("credentials = %#v", fake.credentials)
	}
}
