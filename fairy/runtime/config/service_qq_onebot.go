//go:build !endpointstrict

package config

func (s *ConfigService) QQOneBotSettings() (QQOneBotSettings, error) {
	return ReadQQOneBotSettings(s.root)
}

func (s *ConfigService) SaveQQOneBotSettings(settings QQOneBotSettings) (QQOneBotSettings, error) {
	return WriteQQOneBotSettings(s.root, settings)
}
