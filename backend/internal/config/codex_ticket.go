package config

import (
	"errors"
	"net/url"
	"strconv"
	"strings"
)

// Validate checks the same ticket policy for file/env and live admin settings.
func (c OpenAICodexTicketConfig) Validate() error {
	if err := c.ValidateOptions(); err != nil {
		return err
	}
	if c.TargetLength < 16 || c.TargetLength > 8192 || c.MaxConcurrency < 1 || c.MaxConcurrency > 32 {
		return errors.New("target_length must be 16–8192 and max_concurrency must be 1–32")
	}
	return ValidateCodexTicketHarvestProxyURL(c.HarvestProxyURL)
}

func (o OpenAICodexTicketConfig) ValidateOptions() error {

	if len(o.Models) == 0 || len(o.Models) > 20 {
		return errors.New("models must contain 1–20 model names")
	}
	seen := map[string]bool{}
	for _, model := range o.Models {
		if strings.TrimSpace(model) != model || model == "" || len(model) > 128 || strings.ContainsAny(model, "\r\n\x00") || seen[model] {
			return errors.New("model names must be non-empty, unique, trimmed and at most 128 bytes")
		}
		seen[model] = true
	}
	if len(o.Instructions) > 8000 || strings.TrimSpace(o.UserPrompt) == "" || len(o.UserPrompt) > 8000 {
		return errors.New("user_prompt is required; prompts must not exceed 8000 bytes")
	}
	if o.HarvestProbeIntervalSeconds < 1 || o.HarvestProbeIntervalSeconds > 3600 {
		return errors.New("retry_seconds must be between 1 and 3600")
	}
	if o.HarvestAttemptTimeoutSeconds < 1 || o.HarvestAttemptTimeoutSeconds > 120 {
		return errors.New("timeout_seconds must be between 1 and 120")
	}
	if o.TTLSeconds < 60 || o.TTLSeconds > 86400 {
		return errors.New("ttl_seconds must be between 60 and 86400")
	}
	if o.RefreshBeforeSeconds < 0 || o.RefreshBeforeSeconds >= o.TTLSeconds {
		return errors.New("refresh_before_seconds must be non-negative and smaller than ttl_seconds")
	}
	if o.MaxBackoffSeconds < o.HarvestProbeIntervalSeconds || o.MaxBackoffSeconds > 86400 {
		return errors.New("max_backoff_seconds must be between retry_seconds and 86400")
	}
	return nil
}

func ValidateCodexTicketHarvestProxyURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" || parsed.Opaque != "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return errors.New("harvest proxy must be an HTTP(S) or SOCKS5(h) URL with a host and no path, query or fragment")
	}
	switch parsed.Scheme {
	case "http", "https", "socks5", "socks5h":
	default:
		return errors.New("harvest proxy scheme must be http, https, socks5 or socks5h")
	}
	if port := parsed.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return errors.New("harvest proxy port must be between 1 and 65535")
		}
	}
	return nil
}
