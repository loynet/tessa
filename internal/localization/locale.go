package localization

import "fmt"

type Locale string

const (
	English            Locale = "en"
	PortuguesePortugal Locale = "pt-PT"
)

func Parse(value string) (Locale, error) {
	switch Locale(value) {
	case English, PortuguesePortugal:
		return Locale(value), nil
	default:
		return "", fmt.Errorf("locale must be %q or %q", English, PortuguesePortugal)
	}
}
