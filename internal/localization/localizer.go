package localization

import "fmt"

type Key string

const (
	AssistantTemporaryFailure  Key = "assistant.temporary_failure"
	AssistantUnexpectedFailure Key = "assistant.unexpected_failure"
	AssistantTooLong           Key = "assistant.too_long"
	AssistantRateLimited       Key = "assistant.rate_limited"
	AssistantFiltered          Key = "assistant.filtered"
)

var portuguesePortugal = map[Key]string{
	AssistantTemporaryFailure:  "Não consegui responder agora.",
	AssistantUnexpectedFailure: "Não consegui responder agora. Pelos vistos, até as máquinas têm dias maus.",
	AssistantTooLong:           "Essa mensagem é demasiado longa. Um pouco de contenção não te mata.",
	AssistantRateLimited:       "Calma. Só consigo aguentar uma certa quantidade de ti de cada vez.",
	AssistantFiltered:          "Não posso ajudar com esse pedido. Sim, até eu tenho limites.",
}

type Localizer struct {
	locale Locale
}

func New(locale Locale) Localizer {
	return Localizer{locale: locale}
}

func (l Localizer) Text(key Key, english string) string {
	if l.locale == PortuguesePortugal {
		if translated, ok := portuguesePortugal[key]; ok {
			return translated
		}
	}
	return english
}

func (l Localizer) Format(key Key, english string, args ...any) string {
	return fmt.Sprintf(l.Text(key, english), args...)
}
