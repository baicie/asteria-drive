package cicheck

import (
	"io"
	"regexp"
	"sort"
	"strings"
)

var genericLogRedactions = []struct {
	pattern     *regexp.Regexp
	replacement string
}{
	{regexp.MustCompile(`(?i)https?://[^\s"'<>\\]*[?&](?:X-Amz-|AWSAccessKeyId=)[^\s"'<>\\]*`), "[REDACTED_SIGNED_URL]"},
	{regexp.MustCompile(`(?i)postgres(?:ql)?://[^\s"'<>\\]+`), "[REDACTED_DATABASE_URL]"},
	{regexp.MustCompile(`(?i)(authorization\s*[:=]\s*)(?:bearer\s+)?[^\s,;"'<>\\]+`), "${1}[REDACTED]"},
}

func SanitizeLog(reader io.Reader, writer io.Writer, secrets []string) error {
	contents, err := io.ReadAll(reader)
	if err != nil {
		return err
	}
	sanitized := string(contents)
	for _, redaction := range genericLogRedactions {
		sanitized = redaction.pattern.ReplaceAllString(sanitized, redaction.replacement)
	}
	nonempty := make([]string, 0, len(secrets))
	for _, secret := range secrets {
		if secret != "" {
			nonempty = append(nonempty, secret)
		}
	}
	sort.Slice(nonempty, func(i, j int) bool { return len(nonempty[i]) > len(nonempty[j]) })
	for _, secret := range nonempty {
		sanitized = strings.ReplaceAll(sanitized, secret, "[REDACTED]")
	}
	_, err = io.WriteString(writer, sanitized)
	return err
}
