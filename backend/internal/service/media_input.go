package service

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/netip"
	"net/url"
	"strings"
)

// ValidateMediaInputURL checks a reference without fetching or uploading it.
// Provider adapters choose whether inline data and existing asset IDs are valid.
// Public hostnames are resolved by the provider, not by this gateway.
func ValidateMediaInputURL(value, kind string, allowData, allowAsset bool) error {
	if value == "" || value != strings.TrimSpace(value) {
		return errors.New("media URL is required and must not contain surrounding whitespace")
	}
	if strings.HasPrefix(value, "data:") {
		if !allowData {
			return fmt.Errorf("inline base64 is not supported for %s by this provider; upload it to your storage and provide a public HTTP(S) URL", kind)
		}
		mediaType, _, err := validateMediaDataURL(value)
		if err != nil {
			return err
		}
		if !strings.HasPrefix(mediaType, kind+"/") {
			return fmt.Errorf("data URL MIME type must be %s/*", kind)
		}
		return nil
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed == nil {
		return errors.New("invalid media URL")
	}
	if parsed.Scheme == "asset" && allowAsset {
		if parsed.Hostname() == "" || parsed.User != nil || parsed.Port() != "" || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
			return errors.New("asset reference must use asset://<asset_id>")
		}
		return nil
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return errors.New("media URL must be public HTTP(S) or a supported data:<mime>;base64,<content> URL; local paths and raw base64 are not accepted")
	}
	if parsed.Hostname() == "" || parsed.User != nil || parsed.Opaque != "" {
		return errors.New("media URL must have a public host and must not include username or password credentials")
	}
	host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") {
		return errors.New("media URL must use a public host")
	}
	if address, err := netip.ParseAddr(host); err == nil {
		address = address.Unmap()
		if !address.IsGlobalUnicast() || address.IsPrivate() || address.IsLoopback() || address.IsLinkLocalUnicast() {
			return errors.New("media URL must use a public host")
		}
	} else {
		// Disallow alternative numeric IPv4 spellings such as 127.1 or
		// 0177.0.0.1 that different HTTP implementations may interpret locally.
		allNumeric := true
		for _, char := range host {
			if (char < '0' || char > '9') && char != '.' {
				allNumeric = false
				break
			}
		}
		if allNumeric || !strings.Contains(host, ".") {
			return errors.New("media URL must use a public host")
		}
	}
	return nil
}

// validateMediaDataURL validates standard padded base64 without allocating a
// second copy of a potentially large uploaded image/audio payload.
func validateMediaDataURL(value string) (string, int64, error) {
	header, encoded, found := strings.Cut(value, ",")
	if !found || !strings.HasPrefix(header, "data:") || !strings.HasSuffix(header, ";base64") {
		return "", 0, errors.New("inline media must use data:<mime>;base64,<content>")
	}
	mediaType, params, err := mime.ParseMediaType(strings.TrimSuffix(strings.TrimPrefix(header, "data:"), ";base64"))
	if err != nil || !strings.Contains(mediaType, "/") || len(params) != 0 {
		return "", 0, errors.New("data URL must include a valid MIME type without extra parameters")
	}
	if encoded == "" || strings.ContainsAny(encoded, " \t\r\n") {
		return "", 0, errors.New("data URL must contain non-empty standard base64 without whitespace")
	}
	size, err := io.Copy(io.Discard, base64.NewDecoder(base64.StdEncoding.Strict(), strings.NewReader(encoded)))
	if err != nil || size == 0 {
		return "", 0, errors.New("data URL contains invalid or empty base64")
	}
	return mediaType, size, nil
}
